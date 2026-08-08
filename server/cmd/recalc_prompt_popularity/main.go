// recalc_prompt_popularity walks every published Prompt and rewrites two
// derived columns:
//
//   popular_score — a flat blend of view / copy / like / rating signals.
//                   Weights are tunable via flags; the default favors
//                   commit-style engagement (copy + like) over passive
//                   views, and weights ratings by ratingCount so a single
//                   5-star vote can't pump a row's score.
//
//   is_trending   — set on the top --trending-cutoff rows (by score)
//                   created within --trending-window days. Older rows
//                   with high lifetime scores stay in popular_score
//                   ordering but lose the "trending" badge.
//
// Idempotent: re-running on a fresh dataset converges to the same values.
// Run as a cron / k8s CronJob; the wall-clock cost is one full table scan
// per invocation (batched at --batch-size rows / tx).
//
// Usage:
//   cd server && go run ./cmd/recalc_prompt_popularity                  # full run with defaults
//   cd server && go run ./cmd/recalc_prompt_popularity --dry-run        # log plan, write nothing
//   cd server && go run ./cmd/recalc_prompt_popularity \
//     --view-weight=0.1 --copy-weight=1 --like-weight=2 --rating-weight=5 \
//     --trending-cutoff=50 --trending-window=30
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"server/config"
	"server/model"

	"github.com/spf13/viper"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

type weights struct {
	view   float64
	copy   float64
	like   float64
	rating float64
}

func computeScore(p model.Prompt, w weights) int {
	rating := float64(p.Rating)
	if rating < 0 {
		rating = 0
	}
	score := float64(p.ViewCount)*w.view +
		float64(p.CopyCount)*w.copy +
		float64(p.LikeCount)*w.like +
		rating*float64(p.RatingCount)*w.rating
	if score < 0 {
		score = 0
	}
	return int(score + 0.5)
}

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	viewWeight := flag.Float64("view-weight", 0.1, "weight applied to view_count")
	copyWeight := flag.Float64("copy-weight", 1.0, "weight applied to copy_count")
	likeWeight := flag.Float64("like-weight", 2.0, "weight applied to like_count")
	ratingWeight := flag.Float64("rating-weight", 5.0, "weight applied to rating × rating_count")
	trendingCutoff := flag.Int("trending-cutoff", 50, "mark top N recent rows as is_trending")
	trendingWindow := flag.Int("trending-window", 30, "only rows created within this many days are eligible to be trending")
	batchSize := flag.Int("batch-size", 200, "rows per write transaction")
	dryRun := flag.Bool("dry-run", false, "report intended writes without touching the table")
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

	w := weights{
		view: *viewWeight, copy: *copyWeight, like: *likeWeight, rating: *ratingWeight,
	}

	var total int64
	if err := db.Model(&model.Prompt{}).Where("status = ?", 1).Count(&total).Error; err != nil {
		fmt.Fprintf(os.Stderr, "count published prompts: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Recomputing popular_score for %d published prompts (dry-run=%v)\n", total, *dryRun)

	scoreUpdated := 0
	processed := 0

	for offset := 0; ; offset += *batchSize {
		var batch []model.Prompt
		if err := db.
			Where("status = ?", 1).
			Order("id ASC").
			Offset(offset).
			Limit(*batchSize).
			Find(&batch).Error; err != nil {
			fmt.Fprintf(os.Stderr, "fetch batch at offset %d: %v\n", offset, err)
			os.Exit(1)
		}
		if len(batch) == 0 {
			break
		}

		tx := db.Begin()
		for i := range batch {
			next := computeScore(batch[i], w)
			processed++
			if next == batch[i].PopularScore {
				continue
			}
			if *dryRun {
				scoreUpdated++
				continue
			}
			if err := tx.Model(&model.Prompt{}).
				Where("id = ?", batch[i].Id).
				UpdateColumn("popular_score", next).Error; err != nil {
				tx.Rollback()
				fmt.Fprintf(os.Stderr, "update prompt id=%d: %v\n", batch[i].Id, err)
				os.Exit(1)
			}
			scoreUpdated++
		}
		if err := tx.Commit().Error; err != nil {
			fmt.Fprintf(os.Stderr, "commit batch: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("popular_score: scanned=%d updated=%d (dry-run=%v)\n", processed, scoreUpdated, *dryRun)

	// Trending: top N recent rows by score. Two writes total — clear all,
	// then set the new cohort. Tiny tables so the lock window is cheap.
	cutoffTime := time.Now().Add(-time.Duration(*trendingWindow) * 24 * time.Hour)

	var trendingIDs []uint
	if err := db.Model(&model.Prompt{}).
		Where("status = ? AND collected_at >= ?", 1, cutoffTime).
		Order("popular_score DESC").
		Limit(*trendingCutoff).
		Pluck("id", &trendingIDs).Error; err != nil {
		fmt.Fprintf(os.Stderr, "select trending cohort: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("trending cohort: %d rows (window=%dd, cutoff=%d)\n",
		len(trendingIDs), *trendingWindow, *trendingCutoff)

	if !*dryRun {
		if err := db.Model(&model.Prompt{}).
			Where("is_trending = ?", true).
			UpdateColumn("is_trending", false).Error; err != nil {
			fmt.Fprintf(os.Stderr, "clear trending flags: %v\n", err)
			os.Exit(1)
		}
		if len(trendingIDs) > 0 {
			if err := db.Model(&model.Prompt{}).
				Where("id IN ?", trendingIDs).
				UpdateColumn("is_trending", true).Error; err != nil {
				fmt.Fprintf(os.Stderr, "set trending flags: %v\n", err)
				os.Exit(1)
			}
		}
	}

	fmt.Println("done")
}
