// run_tag_stats fires one or more TagStatsScheduler phases and exits.
// Useful after `import_video_prompts` to refresh tag/category counts,
// or to force-tick the daily popularity pass without waiting for 3am.
//
// Phases (composable; default = refresh):
//
//	--refresh           tag + category recompute (default)
//	--popularity        rescore + recut trending/featured
//	--search-vectors    rebuild w_prompt.search_vector — slow on remote
//	                    DB (~25k single-row UPDATEs); only sane over LAN
//	--all               same as --refresh --popularity --search-vectors
//
// Usage (cwd = server/):
//
//	go run ./cmd/run_tag_stats                     # tag + category
//	go run ./cmd/run_tag_stats --popularity        # popularity only
//	go run ./cmd/run_tag_stats --refresh --popularity
//	go run ./cmd/run_tag_stats --all
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"server/config"
	"server/globals"
	"server/scheduler"

	"github.com/spf13/viper"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

// withCmdTimeouts injects MySQL driver-level timeouts into the DSN.
// The shared config.yaml DSN omits these (the long-running server
// process tolerates idle connections), but a one-shot cmd should fail
// fast if the link wedges instead of hanging forever on a TCP read.
func withCmdTimeouts(dsn string) string {
	for _, k := range []string{"timeout", "readTimeout", "writeTimeout"} {
		if strings.Contains(dsn, k+"=") {
			return dsn
		}
	}
	sep := "&"
	if !strings.Contains(dsn, "?") {
		sep = "?"
	}
	return dsn + sep + "timeout=30s&readTimeout=180s&writeTimeout=180s"
}

func main() {
	refresh := flag.Bool("refresh", false, "recompute tag + category counts (default if no phase chosen)")
	popularity := flag.Bool("popularity", false, "rescore popularity + recut trending/featured")
	searchVectors := flag.Bool("search-vectors", false, "rebuild w_prompt.search_vector (slow on remote DB)")
	all := flag.Bool("all", false, "shortcut for --refresh --popularity --search-vectors")
	flag.Parse()

	if *all {
		*refresh, *popularity, *searchVectors = true, true, true
	}
	if !*refresh && !*popularity && !*searchVectors {
		*refresh = true // default phase when nothing chosen
	}

	v := viper.New()
	v.SetConfigFile("config.yaml")
	if err := v.ReadInConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "read config: %v\n", err)
		os.Exit(1)
	}
	var srv config.Server
	if err := v.Unmarshal(&srv); err != nil {
		fmt.Fprintf(os.Stderr, "unmarshal config: %v\n", err)
		os.Exit(1)
	}

	if srv.GormMysqlSystem.Dbname == "" {
		fmt.Fprintln(os.Stderr, "mysql_system.db-name is empty")
		os.Exit(1)
	}
	db, err := gorm.Open(mysql.Open(withCmdTimeouts(srv.GormMysqlSystem.Dsn())), &gorm.Config{
		Logger: gormLogger.Default.LogMode(gormLogger.Warn),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}

	if globals.GraDBs == nil {
		globals.GraDBs = map[string]*gorm.DB{}
	}
	globals.GraDBs["system"] = db

	s := scheduler.NewTagStatsScheduler()
	if *refresh {
		s.RunRefresh()
	}
	if *popularity {
		s.RunDaily()
	}
	if *searchVectors {
		s.RunSearchVectors()
	}
}
