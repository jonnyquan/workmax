// backfill_image_dims reads image files referenced by w_prompt.local_image
// and writes back image_width, image_height, and aspect_ratio for rows
// that currently have image_width=0. Idempotent.
//
// Usage:
//   cd server && go run ./cmd/backfill_image_dims                # all rows
//   cd server && go run ./cmd/backfill_image_dims --dry-run      # plan only
//   cd server && go run ./cmd/backfill_image_dims --root=./      # uploads root
package main

import (
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	_ "golang.org/x/image/webp"

	"server/config"

	"github.com/spf13/viper"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

type pathRow struct {
	LocalImage string `gorm:"column:local_image"`
}

func aspectRatioOf(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	r := float64(w) / float64(h)
	switch {
	case r > 1.2:
		return "wide"
	case r < 0.84:
		return "tall"
	default:
		return "square"
	}
}

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	root := flag.String("root", ".", "filesystem root containing 'uploads/...' relative paths")
	workers := flag.Int("workers", 8, "concurrent file readers")
	dryRun := flag.Bool("dry-run", false, "report scope without writing")
	flag.Parse()

	v := viper.New()
	v.SetConfigFile(*cfgPath)
	if err := v.ReadInConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "read config %q: %v\n", *cfgPath, err)
		os.Exit(1)
	}

	var srv config.Server
	if err := v.Unmarshal(&srv); err != nil {
		fmt.Fprintf(os.Stderr, "unmarshal config: %v\n", err)
		os.Exit(1)
	}

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

	// 1. Find distinct local_image values needing dims
	var paths []pathRow
	if err := db.Table("w_prompt").
		Select("DISTINCT local_image").
		Where("image_width = 0 AND local_image <> ''").
		Find(&paths).Error; err != nil {
		fmt.Fprintf(os.Stderr, "fetch distinct paths: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Distinct local_image paths to process: %d\n", len(paths))
	if *dryRun {
		// preview
		for i, p := range paths {
			if i >= 5 {
				break
			}
			fmt.Printf("  - %s\n", p.LocalImage)
		}
		fmt.Println("Dry-run complete.")
		return
	}

	type job struct{ path string }
	jobs := make(chan job, *workers*4)

	var ok atomic.Int64
	var missing atomic.Int64
	var decodeErr atomic.Int64
	var updated atomic.Int64

	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				abs := filepath.Join(*root, j.path)
				if !strings.HasPrefix(j.path, "uploads/") && !strings.HasPrefix(j.path, "/") {
					abs = filepath.Join(*root, j.path)
				}
				f, err := os.Open(abs)
				if err != nil {
					missing.Add(1)
					continue
				}
				cfg, _, err := image.DecodeConfig(f)
				f.Close()
				if err != nil {
					decodeErr.Add(1)
					continue
				}
				if cfg.Width <= 0 || cfg.Height <= 0 {
					decodeErr.Add(1)
					continue
				}
				ar := aspectRatioOf(cfg.Width, cfg.Height)
				res := db.Table("w_prompt").
					Where("local_image = ? AND image_width = 0", j.path).
					Updates(map[string]any{
						"image_width":  cfg.Width,
						"image_height": cfg.Height,
						"aspect_ratio": ar,
					})
				if res.Error != nil {
					decodeErr.Add(1)
					continue
				}
				ok.Add(1)
				updated.Add(res.RowsAffected)
			}
		}()
	}

	for _, p := range paths {
		jobs <- job{path: p.LocalImage}
	}
	close(jobs)
	wg.Wait()

	fmt.Printf("\nDone. distinct=%d ok=%d missing=%d decode_err=%d rows_updated=%d\n",
		len(paths), ok.Load(), missing.Load(), decodeErr.Load(), updated.Load())
}
