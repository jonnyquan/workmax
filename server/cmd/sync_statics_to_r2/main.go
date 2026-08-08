// sync_statics_to_r2 walks one or more local directories and uploads every
// file to the `statics` R2 bucket configured under `statics.r2` in
// config.yaml. Object keys preserve the relative path under --prefix so the
// CDN URL becomes `<public_url>/<prefix>/<relative-path>`.
//
// Idempotent: HEADs each remote object first and skips when the local MD5
// matches the remote ETag. Re-running after a partial run is safe.
//
// Usage (cwd = server/):
//
//	# Prompt mirror — lives at bucket root so multiple apps can share
//	# the same imagery; key matches the /uploads/prompts/* URL the API
//	# already emits, so DB rows stay untouched.
//	go run ./cmd/sync_statics_to_r2 --src ./uploads/prompts --prefix uploads/prompts
//
// Flags:
//
//	--config       path to config.yaml (default config.yaml)
//	--src          local directory to upload (required)
//	--prefix       Server-owned remote key prefix; trailing slash optional
//	--workers      concurrent uploaders (default 8)
//	--cache        Cache-Control header (default "public, max-age=31536000, immutable")
//	--dry-run      list what would be uploaded without writing
//	--force        upload even if remote ETag matches local MD5
package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"server/config"
	"server/service/storage"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/spf13/viper"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	srcDir := flag.String("src", "", "local directory to upload (required)")
	prefix := flag.String("prefix", "", "remote key prefix (e.g. images, videos, uploads/prompts)")
	workers := flag.Int("workers", 8, "concurrent uploaders")
	cacheCtrl := flag.String("cache", "public, max-age=31536000, immutable", "Cache-Control header")
	dryRun := flag.Bool("dry-run", false, "report scope without writing")
	force := flag.Bool("force", false, "skip remote ETag check; always re-upload")
	timeoutMin := flag.Int("timeout-min", 5, "per-file upload timeout in minutes")
	flag.Parse()

	if strings.TrimSpace(*srcDir) == "" {
		fmt.Fprintln(os.Stderr, "--src is required")
		os.Exit(2)
	}
	cleanPrefix := strings.Trim(*prefix, "/")

	srv := mustLoadConfig(*cfgPath)
	store, client := mustBuildStaticsStore(srv.Statics)

	files := mustListFiles(*srcDir)
	fmt.Printf("Discovered %d files under %s\n", len(files), *srcDir)
	if *dryRun {
		for _, f := range files {
			key := joinKey(cleanPrefix, f.rel)
			fmt.Printf("  upload %s -> %s\n", f.abs, key)
		}
		return
	}

	jobs := make(chan localFile)
	var wg sync.WaitGroup
	var uploaded, skipped, failed int64
	start := time.Now()

	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				key := joinKey(cleanPrefix, f.rel)
				ok, err := uploadOne(store, client, f, key, *cacheCtrl, *force, time.Duration(*timeoutMin)*time.Minute)
				if err != nil {
					atomic.AddInt64(&failed, 1)
					fmt.Fprintf(os.Stderr, "  ✗ %s: %v\n", key, err)
					continue
				}
				if ok {
					atomic.AddInt64(&uploaded, 1)
					fmt.Printf("  ↑ %s (%d bytes)\n", key, f.size)
				} else {
					atomic.AddInt64(&skipped, 1)
				}
			}
		}()
	}

	for _, f := range files {
		jobs <- f
	}
	close(jobs)
	wg.Wait()

	fmt.Printf("\nDone in %s. uploaded=%d skipped=%d failed=%d\n",
		time.Since(start).Round(time.Millisecond), uploaded, skipped, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

// ----- config + store -----

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

func mustBuildStaticsStore(s config.Statics) (storage.ObjectStore, *s3.Client) {
	r2 := s.R2
	if strings.TrimSpace(r2.Endpoint) == "" || strings.TrimSpace(r2.Bucket) == "" {
		fmt.Fprintln(os.Stderr, "statics.r2 is not configured (endpoint/bucket empty)")
		os.Exit(1)
	}
	cfg := storage.ObjectStoreConfig{
		Provider:                "r2",
		Endpoint:                r2.Endpoint,
		Region:                  r2.Region,
		Bucket:                  r2.Bucket,
		AccessKeyID:             r2.AccessKeyID,
		SecretAccessKey:         r2.SecretAccessKey,
		PublicURL:               r2.PublicURL,
		ForcePathStyle:          true,
		MultipartThresholdBytes: storage.NormalizeMultipartThresholdBytes(int64(r2.MultipartThresholdMB) * 1024 * 1024),
		MultipartPartSizeBytes:  storage.NormalizeMultipartPartSizeBytes(int64(r2.MultipartPartSizeMB) * 1024 * 1024),
	}
	store, _, err := storage.NewObjectStoreFromConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build statics store: %v\n", err)
		os.Exit(1)
	}
	r2Store, ok := store.(*storage.R2CompatibleStore)
	if !ok {
		fmt.Fprintln(os.Stderr, "expected R2CompatibleStore")
		os.Exit(1)
	}
	return store, r2Store.RawClient()
}

// ----- discovery -----

type localFile struct {
	abs  string
	rel  string
	size int64
}

func mustListFiles(root string) []localFile {
	abs, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "abs(%s): %v\n", root, err)
		os.Exit(1)
	}
	info, err := os.Stat(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stat(%s): %v\n", abs, err)
		os.Exit(1)
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "%s is not a directory\n", abs)
		os.Exit(1)
	}
	var out []localFile
	err = filepath.Walk(abs, func(p string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if fi.IsDir() {
			if strings.HasPrefix(fi.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(fi.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return err
		}
		out = append(out, localFile{abs: p, rel: filepath.ToSlash(rel), size: fi.Size()})
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "walk: %v\n", err)
		os.Exit(1)
	}
	return out
}

func joinKey(prefix, rel string) string {
	if prefix == "" {
		return rel
	}
	return prefix + "/" + rel
}

// ----- upload -----

func uploadOne(store storage.ObjectStore, client *s3.Client, f localFile, key, cacheCtrl string, force bool, timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if !force {
		same, err := remoteMatches(ctx, client, store.Bucket(), key, f.abs)
		if err == nil && same {
			return false, nil
		}
	}

	fh, err := os.Open(f.abs)
	if err != nil {
		return false, err
	}
	defer fh.Close()

	contentType := mime.TypeByExtension(filepath.Ext(f.abs))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err = store.Put(ctx, &storage.PutObjectRequest{
		Key:          key,
		Body:         fh,
		Size:         f.size,
		ContentType:  contentType,
		CacheControl: cacheCtrl,
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// remoteMatches HEADs the object and compares its ETag to the local MD5.
// R2 returns the MD5 hex as the ETag for non-multipart uploads, which
// covers everything below the multipart threshold (default 64 MB).
func remoteMatches(ctx context.Context, client *s3.Client, bucket, key, localPath string) (bool, error) {
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) {
			return false, nil
		}
		return false, err
	}
	if head.ETag == nil {
		return false, nil
	}
	remoteETag := strings.Trim(*head.ETag, "\"")
	if strings.Contains(remoteETag, "-") {
		// Multipart upload — ETag isn't a plain MD5, fall back to size compare.
		if head.ContentLength != nil {
			fi, err := os.Stat(localPath)
			if err == nil && *head.ContentLength == fi.Size() {
				return true, nil
			}
		}
		return false, nil
	}
	localMD5, err := fileMD5(localPath)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(remoteETag, localMD5), nil
}

func fileMD5(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	h := md5.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
