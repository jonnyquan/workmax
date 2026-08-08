// mirror_prompt_assets downloads remote http(s) URLs referenced by
// w_prompt.cover_image / local_image / thumb_image / preview_video,
// stores them under ./uploads/prompts/mirror keyed by SHA256, and
// rewrites the rows to point at the mirrored copies so upstream link
// rot can no longer break the prompt library.
//
// Mirrors the nanobanano layout (./uploads/prompts/nanobanano/...)
// served by /uploads/* through ServeUploadedFile in core/server.go.
//
// Usage (cwd = server/):
//
//	go run ./cmd/mirror_prompt_assets --dry-run
//	go run ./cmd/mirror_prompt_assets
//	go run ./cmd/mirror_prompt_assets --limit=20 --workers=4
//	go run ./cmd/mirror_prompt_assets --out=./uploads/prompts/mirror
//
// Idempotent: rows already pointing under --url-prefix are skipped
// and per-process SHA cache makes CDN aliases collapse to one
// download/write.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"server/config"

	"github.com/spf13/viper"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

const (
	tableName       = "w_prompt"
	defaultOutDir   = "./uploads/prompts/mirror"
	defaultURLPath  = "/uploads/prompts/mirror"
	defaultMaxBytes = 100 * 1024 * 1024 // 100MB; covers cdn.openai.com sora videos
	defaultTimeout  = 90 * time.Second
)

var assetColumns = []string{"cover_image", "local_image", "thumb_image", "preview_video"}

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	outDir := flag.String("out", defaultOutDir, "filesystem root for mirrored assets")
	urlPrefix := flag.String("url-prefix", defaultURLPath, "URL path prefix written into DB (matches static mount)")
	workers := flag.Int("workers", 6, "concurrent downloaders")
	dryRun := flag.Bool("dry-run", false, "report scope without downloading or writing")
	limit := flag.Int("limit", 0, "process at most N distinct URLs (0 = unlimited)")
	maxBytes := flag.Int64("max-bytes", defaultMaxBytes, "skip assets larger than this many bytes")
	timeoutSecs := flag.Int("timeout", int(defaultTimeout/time.Second), "per-asset HTTP timeout in seconds")
	flag.Parse()

	*outDir = strings.TrimRight(*outDir, "/")
	*urlPrefix = "/" + strings.Trim(*urlPrefix, "/")

	srv := mustLoadConfig(*cfgPath)
	db := mustOpenDB(srv)

	urls := collectRemoteURLs(db, *urlPrefix)
	fmt.Printf("Distinct remote URLs needing mirror: %d\n", len(urls))
	if *limit > 0 && len(urls) > *limit {
		urls = urls[:*limit]
		fmt.Printf("Limiting to first %d.\n", *limit)
	}
	if len(urls) == 0 {
		fmt.Println("Nothing to do.")
		return
	}
	if *dryRun {
		for i, u := range urls {
			if i >= 10 {
				fmt.Printf("  ... (%d more)\n", len(urls)-10)
				break
			}
			fmt.Printf("  - %s\n", u)
		}
		fmt.Println("Dry-run complete.")
		return
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir %s: %v\n", *outDir, err)
		os.Exit(1)
	}

	httpClient := &http.Client{Timeout: time.Duration(*timeoutSecs) * time.Second}

	var mirrored, skipped, failed atomic.Int64
	var rowsUpdated atomic.Int64

	jobs := make(chan string, *workers*4)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for upstream := range jobs {
				newURL, err := mirrorOne(httpClient, upstream, *outDir, *urlPrefix, *maxBytes)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  fail: %s — %v\n", upstream, err)
					failed.Add(1)
					continue
				}
				if newURL == "" {
					skipped.Add(1)
					continue
				}
				rows, err := rewriteRows(db, upstream, newURL)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  rewrite fail: %s — %v\n", upstream, err)
					failed.Add(1)
					continue
				}
				mirrored.Add(1)
				rowsUpdated.Add(rows)
				fmt.Printf("  ok: %s -> %s (rows=%d)\n", upstream, newURL, rows)
			}
		}()
	}
	for _, u := range urls {
		jobs <- u
	}
	close(jobs)
	wg.Wait()

	fmt.Printf("\nDone. distinct=%d mirrored=%d skipped=%d failed=%d rows_updated=%d\n",
		len(urls), mirrored.Load(), skipped.Load(), failed.Load(), rowsUpdated.Load())
}

// ----- bootstrap -----

func mustLoadConfig(path string) config.Server {
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "read config %q: %v\n", path, err)
		os.Exit(1)
	}
	var srv config.Server
	if err := v.Unmarshal(&srv); err != nil {
		fmt.Fprintf(os.Stderr, "unmarshal config: %v\n", err)
		os.Exit(1)
	}
	return srv
}

func mustOpenDB(srv config.Server) *gorm.DB {
	m := srv.GormMysqlSystem
	if m.Dbname == "" {
		fmt.Fprintln(os.Stderr, "mysql_system.db-name is empty")
		os.Exit(1)
	}
	db, err := gorm.Open(mysql.Open(m.Dsn()), &gorm.Config{
		Logger: gormLogger.Default.LogMode(gormLogger.Warn),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	return db
}

// ----- discover -----

// collectRemoteURLs gathers distinct http(s) URLs across the asset
// columns, filtering out anything that already lives under the
// configured url-prefix so reruns are no-ops.
func collectRemoteURLs(db *gorm.DB, urlPrefix string) []string {
	seen := map[string]struct{}{}
	for _, col := range assetColumns {
		var values []string
		query := db.Table(tableName).
			Distinct(col).
			Where(col+" LIKE 'http%'")
		if urlPrefix != "" {
			query = query.Where(col+" NOT LIKE ?", urlPrefix+"%")
		}
		if err := query.Pluck(col, &values).Error; err != nil {
			fmt.Fprintf(os.Stderr, "scan %s: %v\n", col, err)
			os.Exit(1)
		}
		for _, raw := range values {
			u := strings.TrimSpace(raw)
			if u == "" {
				continue
			}
			seen[u] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for u := range seen {
		out = append(out, u)
	}
	return out
}

// ----- mirror -----

// mirrorCache keeps in-process dedup across workers in case two
// distinct upstream URLs resolve to the same SHA (CDN aliases).
var (
	mirrorCacheMu sync.Mutex
	mirrorCache   = map[string]string{} // sha -> publicURL
)

func mirrorOne(client *http.Client, upstream, outDir, urlPrefix string, maxBytes int64) (string, error) {
	body, contentType, err := fetch(client, upstream, maxBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	ext := pickExtension(contentType, upstream)
	relPath := filepath.Join(sha[:2], sha+ext)

	mirrorCacheMu.Lock()
	if cached, ok := mirrorCache[sha]; ok {
		mirrorCacheMu.Unlock()
		return cached, nil
	}
	mirrorCacheMu.Unlock()

	dest := filepath.Join(outDir, relPath)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	// Skip the write if the bytes are already on disk (rerun after
	// partial DB-update failure) — disk wins, content-addressed.
	if _, err := os.Stat(dest); err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("stat: %w", err)
		}
		if err := os.WriteFile(dest, body, 0o644); err != nil {
			return "", fmt.Errorf("write: %w", err)
		}
	}

	publicURL := strings.TrimRight(urlPrefix, "/") + "/" + strings.ReplaceAll(relPath, string(filepath.Separator), "/")

	mirrorCacheMu.Lock()
	mirrorCache[sha] = publicURL
	mirrorCacheMu.Unlock()

	return publicURL, nil
}

func fetch(client *http.Client, rawURL string, maxBytes int64) ([]byte, string, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "workmax-prompt-mirror/1.0 (+contact@workmax.app)")
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	if resp.ContentLength > 0 && resp.ContentLength > maxBytes {
		return nil, "", fmt.Errorf("size %d > max %d", resp.ContentLength, maxBytes)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) > maxBytes {
		return nil, "", fmt.Errorf("body exceeds max %d", maxBytes)
	}
	return body, strings.TrimSpace(resp.Header.Get("Content-Type")), nil
}

func pickExtension(contentType, rawURL string) string {
	mt, _, _ := mime.ParseMediaType(contentType)
	switch strings.ToLower(mt) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/avif":
		return ".avif"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	}
	if u, err := url.Parse(rawURL); err == nil {
		if ext := strings.ToLower(filepath.Ext(u.Path)); ext != "" && len(ext) <= 5 {
			return ext
		}
	}
	return ".bin"
}

// ----- rewrite -----

// rewriteRows updates every w_prompt row whose asset columns equal the
// upstream URL, replacing them with the mirrored URL. Returns total
// rows touched across all four columns.
func rewriteRows(db *gorm.DB, upstream, mirroredURL string) (int64, error) {
	var total int64
	for _, col := range assetColumns {
		res := db.Table(tableName).
			Where(col+" = ?", upstream).
			Update(col, mirroredURL)
		if res.Error != nil {
			return total, fmt.Errorf("update %s: %w", col, res.Error)
		}
		total += res.RowsAffected
	}
	if total == 0 {
		return 0, errors.New("no rows matched after fetch — concurrent rewrite?")
	}
	return total, nil
}
