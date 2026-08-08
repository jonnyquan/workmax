// import_video_prompts ingests video prompts collected from curated GitHub
// repositories into the w_prompt table.
//
// Currently wired sources:
//
//	hr98w/awesome-sora-prompts            — text_to_video, primary sora-2
//	jax-explorer/awesome-veo3-videos      — text_to_video, primary veo-3.1
//	maciejdzierzek/kling-ai-prompt-generator — text_to_video AND image_to_video, primary kling-2.6
//
// For each parsed prompt the importer writes two rows — `lang=en` (the
// canonical source) and `lang=zh` (LLM-translated title + content +
// description) — sharing the same slug.
//
// Usage:
//
//	cd server && go run ./cmd/import_video_prompts                   # all sources
//	cd server && go run ./cmd/import_video_prompts --dry-run         # parse + plan only
//	cd server && go run ./cmd/import_video_prompts --source=sora     # one source
//	cd server && go run ./cmd/import_video_prompts --skip-translate  # en only
//	cd server && go run ./cmd/import_video_prompts --status=0        # land as 待审核
//	cd server && go run ./cmd/import_video_prompts --force           # overwrite existing
//
// Idempotency: rows are matched by (source_id, lang). Without --force,
// existing pairs are skipped — rerunning picks up only what's new upstream.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"server/config"
	"server/globals"
	"server/model"
	"server/scheduler"
	llmService "server/service/llm"

	"github.com/spf13/viper"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormLogger "gorm.io/gorm/logger"
)

const defaultSort = -5 // auto-imports rank below curated rows (sort >= 45)

// supportedTargetLangs is the Server-owned prompt locale catalog. The importer
// can write a row per lang per prompt; non-en langs come from parser-provided
// Translations or fall back to LLM.
var supportedTargetLangs = []string{
	"en", "zh", "ja", "ko", "th", "vi", "es", "de", "fr", "it", "pt", "tr",
	"ar", "he", "nl", "pl", "ru", "sv",
}

// langDisplayName feeds the LLM "target language: ..." line.
var langDisplayName = map[string]string{
	"en": "English", "zh": "Simplified Chinese", "ja": "Japanese", "ko": "Korean",
	"th": "Thai", "vi": "Vietnamese", "es": "Spanish", "de": "German",
	"fr": "French", "it": "Italian", "pt": "Portuguese", "tr": "Turkish",
	"ar": "Arabic", "he": "Hebrew", "nl": "Dutch", "pl": "Polish",
	"ru": "Russian", "sv": "Swedish",
}

// sourceSpec encodes the fixed metadata for a curated GitHub source —
// what model it targets, what default fields to fill on the row, and what
// slug prefix / tag to attach. Every parsedPrompt carries a pointer to one.
type sourceSpec struct {
	Name            string   // short id used in --source flag
	RepoSlug        string   // owner/repo, becomes source_id prefix
	PrimaryModel    string   // e.g. sora-2, veo-3.1, kling-2.6
	SupportedModels []string // JSON array stored on the row
	SlugPrefix      string   // e.g. sora, veo3, kling
	Style           string   // default style tag
	ModelParams     string   // default model_params JSON
	Width, Height   int      // default image_width / image_height
	AspectRatio     string   // wide / tall / square
	BaseTags        []string // common tags appended to row.tags
	DescTemplate    string   // template for prompt_description; %s = section tag
}

// soraSpec — the original hr98w source, kept verbatim for re-runs.
var soraSpec = &sourceSpec{
	Name:            "sora",
	RepoSlug:        "hr98w/awesome-sora-prompts",
	PrimaryModel:    "sora-2",
	SupportedModels: []string{"sora-2", "veo-3.1", "veo-3", "kling-2.6", "seedance-2", "seedance"},
	SlugPrefix:      "sora",
	Style:           "cinematic",
	ModelParams:     `{"duration":"5","resolution":"1080p"}`,
	Width:           1920,
	Height:          1080,
	AspectRatio:     "wide",
	BaseTags:        []string{"video", "sora"},
	DescTemplate:    "Curated %s sora video prompt.",
}

// veo3Spec — jax-explorer/awesome-veo3-videos.
var veo3Spec = &sourceSpec{
	Name:            "veo3",
	RepoSlug:        "jax-explorer/awesome-veo3-videos",
	PrimaryModel:    "veo-3.1",
	SupportedModels: []string{"veo-3.1", "veo-3", "sora-2", "kling-2.6", "seedance-2"},
	SlugPrefix:      "veo3",
	Style:           "cinematic",
	ModelParams:     `{"duration":"8","resolution":"1080p"}`,
	Width:           1920,
	Height:          1080,
	AspectRatio:     "wide",
	BaseTags:        []string{"video", "veo3", "veo"},
	DescTemplate:    "Curated Veo 3 video prompt — %s.",
}

// gptImage2Spec — image source. Same dual-readme structure as seedance.
var gptImage2Spec = &sourceSpec{
	Name:            "gpt-image-2",
	RepoSlug:        "YouMind-OpenLab/awesome-gpt-image-2",
	PrimaryModel:    "gpt-image-2",
	SupportedModels: []string{"gpt-image-2", "nanobanana-pro", "flux-1.1-pro"},
	SlugPrefix:      "gpti2",
	Style:           "photoreal",
	ModelParams:     `{"resolution":"1024x1024","quality":"high"}`,
	Width:           1024,
	Height:          1024,
	AspectRatio:     "square",
	BaseTags:        []string{"image", "gpt-image-2"},
	DescTemplate:    "Curated GPT Image 2 prompt — %s.",
}

// klingSpec — maciejdzierzek/kling-ai-prompt-generator (examples/*.md).
var klingSpec = &sourceSpec{
	Name:            "kling",
	RepoSlug:        "maciejdzierzek/kling-ai-prompt-generator",
	PrimaryModel:    "kling-2.6",
	SupportedModels: []string{"kling-2.6", "kling-2.5", "veo-3.1", "sora-2", "seedance-2"},
	SlugPrefix:      "kling",
	Style:           "cinematic",
	ModelParams:     `{"duration":"5","resolution":"1080p"}`,
	Width:           1920,
	Height:          1080,
	AspectRatio:     "wide",
	BaseTags:        []string{"video", "kling"},
	DescTemplate:    "Curated Kling 2.x reference prompt — %s.",
}

// seedanceSpec — YouMind-OpenLab/awesome-seedance-2-prompts.
var seedanceSpec = &sourceSpec{
	Name:            "seedance",
	RepoSlug:        "YouMind-OpenLab/awesome-seedance-2-prompts",
	PrimaryModel:    "seedance-2",
	SupportedModels: []string{"seedance-2", "seedance", "veo-3.1", "kling-2.6", "sora-2"},
	SlugPrefix:      "seedance",
	Style:           "cinematic",
	ModelParams:     `{"duration":"10","resolution":"1080p"}`,
	Width:           1920,
	Height:          1080,
	AspectRatio:     "wide",
	BaseTags:        []string{"video", "seedance"},
	DescTemplate:    "Curated Seedance 2.0 video prompt — %s.",
}

var allSpecs = []*sourceSpec{soraSpec, veo3Spec, klingSpec, seedanceSpec, gptImage2Spec}

// soraSection is metadata for the three top-level lists in the sora repo.
type soraSection struct {
	heading string
	slug    string
	tag     string
}

var soraSections = []soraSection{
	{heading: "Official Video Generation Prompts", slug: "official", tag: "official"},
	{heading: "Official Video Generation Prompts (Twitter)", slug: "official-twitter", tag: "twitter"},
	{heading: "Official Video Generation Prompts (Tiktok)", slug: "official-tiktok", tag: "tiktok"},
}

// parsedPrompt is the intermediate form between parser and DB insert.
type parsedPrompt struct {
	Spec        *sourceSpec
	SectionSlug string // varies per source: section tag, file name, or "case"
	SectionTag  string
	PromptType  string // text_to_video | image_to_video
	VideoURL    string // direct mp4 URL when this is a video source
	ImageURL    string // direct image URL when this is an image source
	URLBase     string // basename used in source_id
	Content     string // full English prompt text
	Medium      string // image | video — defaults from spec when blank

	// Optional source-provided English metadata. When non-empty these
	// override the auto-derived fallbacks (titleFromContent, spec template).
	Title       string
	Description string

	// Optional pre-translated fields per non-en language. Lang codes use
	// simple ISO-639 form (zh, ja, ko, ...). When Content is non-empty for
	// a target lang the importer skips the LLM call for that lang.
	Translations map[string]localizedFields
}

// localizedFields is the per-language triple of translated metadata.
type localizedFields struct {
	Title       string
	Content     string
	Description string
}

// hasTranslation reports whether the parser already provided a translation
// for the given lang, letting the importer skip the LLM round trip.
func (p parsedPrompt) hasTranslation(lang string) bool {
	if p.Translations == nil {
		return false
	}
	t, ok := p.Translations[lang]
	return ok && strings.TrimSpace(t.Content) != ""
}

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	limit := flag.Int("limit", 0, "stop after N parsed prompts (0 = all)")
	workers := flag.Int("workers", 4, "concurrent translation workers")
	dryRun := flag.Bool("dry-run", false, "parse + plan, no LLM calls or DB writes")
	skipTranslate := flag.Bool("skip-translate", false, "insert en rows only (no zh)")
	status := flag.Int("status", 1, "row status (0=待审核 1=已发布)")
	baseSort := flag.Int("base-sort", defaultSort, "sort weight for new rows")
	force := flag.Bool("force", false, "overwrite existing rows matching source_id (preserve counters)")
	sourceFilter := flag.String("source", "", "limit to one source name (sora|veo3|kling|seedance)")
	langsFlag := flag.String("langs", "en,zh", "target lang codes — `all` expands to the 18 platform langs, or comma-separated list")
	skipRefresh := flag.Bool("skip-refresh", false, "skip post-import tag/category recompute (for dry-runs / iterative reruns)")
	flag.Parse()

	targetLangs := parseLangsFlag(*langsFlag)
	if len(targetLangs) == 0 {
		fmt.Fprintln(os.Stderr, "no valid target langs (must include 'en')")
		os.Exit(1)
	}
	if targetLangs[0] != "en" {
		// Force en first — it's the canonical row and other langs derive from it.
		hasEn := false
		for _, l := range targetLangs {
			if l == "en" {
				hasEn = true
				break
			}
		}
		if !hasEn {
			targetLangs = append([]string{"en"}, targetLangs...)
		}
	}
	fmt.Printf("Target langs (%d): %s\n", len(targetLangs), strings.Join(targetLangs, ", "))

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

	globals.GraConf = srv
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	globals.GraLog = logger

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

	// Default gorm idle pool is 2 — far too small for parallel workers
	// against a remote DB (each missing idle connection costs a full
	// TCP+auth round-trip, ~1.5s on this box). Size to match worker count.
	if sqlDB, perr := db.DB(); perr == nil {
		sqlDB.SetMaxOpenConns(*workers + 2)
		sqlDB.SetMaxIdleConns(*workers + 2)
		sqlDB.SetConnMaxLifetime(30 * time.Minute)
	}

	// 1. Pick which sources to run
	var specs []*sourceSpec
	for _, s := range allSpecs {
		if *sourceFilter == "" || *sourceFilter == s.Name {
			specs = append(specs, s)
		}
	}
	if len(specs) == 0 {
		fmt.Fprintf(os.Stderr, "no source matches %q (valid: sora, veo3, kling)\n", *sourceFilter)
		os.Exit(1)
	}

	// 2. Fetch + parse each source
	var parsed []parsedPrompt
	for _, spec := range specs {
		fmt.Printf("→ %s (%s)\n", spec.Name, spec.RepoSlug)
		items, err := fetchAndParse(spec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  fetch/parse %s: %v\n", spec.Name, err)
			os.Exit(1)
		}
		fmt.Printf("  parsed %d prompts\n", len(items))
		parsed = append(parsed, items...)
	}
	fmt.Printf("Total parsed: %d\n", len(parsed))

	if *limit > 0 && *limit < len(parsed) {
		parsed = parsed[:*limit]
		fmt.Printf("Limit applied: %d\n", len(parsed))
	}

	// 3. Diff against DB by source_id (one IN-list across all sources)
	type existingRow struct {
		ID       uint
		SourceID string
		Lang     string
	}
	sourceIDs := make([]string, 0, len(parsed))
	for _, p := range parsed {
		sourceIDs = append(sourceIDs, buildSourceID(p))
	}
	var existing []existingRow
	if len(sourceIDs) > 0 {
		if err := db.Table("w_prompt").
			Select("id, source_id, lang").
			Where("source_id IN ?", sourceIDs).
			Scan(&existing).Error; err != nil {
			fmt.Fprintf(os.Stderr, "scan existing: %v\n", err)
			os.Exit(1)
		}
	}
	existingKey := make(map[string]bool, len(existing))
	for _, r := range existing {
		existingKey[r.SourceID+"|"+r.Lang] = true
	}
	fmt.Printf("Existing rows matching: %d\n", len(existing))

	// 4. Build slugs. Reuse existing slug per source_id (idempotent re-runs);
	// resolve collisions only when a NEW source_id would clash with one
	// already owned by a different source_id.
	slugOwners, err := loadSlugMap(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load existing slugs: %v\n", err)
		os.Exit(1)
	}
	slugBySourceID := make(map[string]string, len(slugOwners))
	for slug, sid := range slugOwners {
		// First slug wins per source_id (older row).
		if _, exists := slugBySourceID[sid]; !exists {
			slugBySourceID[sid] = slug
		}
	}
	finalSlug := assignSlugs(parsed, slugBySourceID, slugOwners)

	var planned []plannedItem
	for i, p := range parsed {
		sid := buildSourceID(p)
		slug := finalSlug[i]

		// Decide which langs need writing for this prompt.
		var needLangs []string
		for _, lang := range targetLangs {
			if !*force && existingKey[sid+"|"+lang] {
				continue
			}
			// `--skip-translate` only suppresses non-en langs that would
			// require an LLM call. Pre-translated langs always proceed.
			if lang != "en" && *skipTranslate && !p.hasTranslation(lang) {
				continue
			}
			needLangs = append(needLangs, lang)
		}
		if len(needLangs) == 0 {
			continue
		}
		planned = append(planned, plannedItem{
			parsed:    p,
			sourceID:  sid,
			slug:      slug,
			needLangs: needLangs,
		})
	}

	// Tally per-lang and overall.
	perLang := map[string]int{}
	for _, pl := range planned {
		for _, l := range pl.needLangs {
			perLang[l]++
		}
	}
	parts := make([]string, 0, len(perLang))
	for _, l := range targetLangs {
		if n, ok := perLang[l]; ok {
			parts = append(parts, fmt.Sprintf("%s=%d", l, n))
		}
	}
	fmt.Printf("\nPlanned writes (%d prompts): %s\n", len(planned), strings.Join(parts, " "))

	if *dryRun {
		preview := len(planned)
		if preview > 12 {
			preview = 12
		}
		for i := 0; i < preview; i++ {
			pl := planned[i]
			asset := pl.parsed.VideoURL
			if asset == "" {
				asset = pl.parsed.ImageURL
			}
			fmt.Printf("  [%d] %s (%s)\n      slug=%s  type=%s  model=%s\n      asset=%s\n      content=%s\n",
				i+1, pl.sourceID, pl.parsed.Spec.Name,
				pl.slug, pl.parsed.PromptType, pl.parsed.Spec.PrimaryModel,
				asset, truncate(pl.parsed.Content, 90))
		}
		fmt.Println("\nDry-run complete — no writes.")
		return
	}

	if len(planned) == 0 {
		fmt.Println("Nothing to insert. Exiting.")
		return
	}

	// 5. LLM warm-up
	var llmSvc *llmService.UniversalLLMService
	if !*skipTranslate {
		if !llmService.IsClientReady() {
			fmt.Fprintln(os.Stderr, "LLM client not ready — re-run with --skip-translate to insert en only")
			os.Exit(1)
		}
		llmSvc = llmService.GetService()
	}

	// 6. Worker pool
	type result struct {
		sourceID string
		err      error
		writes   int
	}
	jobs := make(chan plannedItem, *workers*2)
	results := make(chan result, *workers*2)
	var wg sync.WaitGroup
	var ok atomic.Int64
	var failed atomic.Int64

	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				writes, err := processOne(db, llmSvc, j, *baseSort, *status, *force, *skipTranslate)
				if err != nil {
					failed.Add(1)
				} else {
					ok.Add(1)
				}
				results <- result{sourceID: j.sourceID, err: err, writes: writes}
			}
		}()
	}
	go func() {
		for _, pl := range planned {
			jobs <- pl
		}
		close(jobs)
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	startedAt := time.Now()
	processed := 0
	for r := range results {
		processed++
		statusTag := "OK "
		errMsg := ""
		if r.err != nil {
			statusTag = "ERR"
			errMsg = " — " + r.err.Error()
		}
		fmt.Printf("[%s] %s writes=%d%s\n", statusTag, r.sourceID, r.writes, errMsg)
		if processed%10 == 0 || processed == len(planned) {
			elapsed := time.Since(startedAt).Seconds()
			rate := float64(processed) / elapsed
			fmt.Printf("  progress: %d/%d ok=%d failed=%d %.1f/s\n",
				processed, len(planned), ok.Load(), failed.Load(), rate)
		}
	}

	fmt.Printf("\nDone. ok=%d failed=%d total=%d elapsed=%s\n",
		ok.Load(), failed.Load(), len(planned), time.Since(startedAt).Round(time.Second))

	// Refresh tag/category counts so the chip strip and category badges
	// catch up with the rows we just inserted/updated. Cheap (~1 min on
	// remote DB) and prevents stale counts until the next manual run.
	// Skipped on --skip-refresh to keep dry-runs/iterative reruns quick.
	if !*skipRefresh && ok.Load() > 0 {
		fmt.Println()
		if globals.GraDBs == nil {
			globals.GraDBs = map[string]*gorm.DB{}
		}
		globals.GraDBs["system"] = db
		scheduler.NewTagStatsScheduler().RunRefresh()
	}

	if failed.Load() > 0 {
		os.Exit(2)
	}
}

func buildSourceID(p parsedPrompt) string {
	return fmt.Sprintf("%s:%s:%s", p.Spec.RepoSlug, p.SectionSlug, p.URLBase)
}

// loadSlugMap returns slug → source_id for every row in w_prompt. New
// imports check this map to (a) reuse the slug already assigned to a
// known source_id and (b) avoid slug collisions with rows owned by a
// different source_id.
func loadSlugMap(db *gorm.DB) (map[string]string, error) {
	type slugRow struct {
		Slug     string
		SourceID string
	}
	var rows []slugRow
	if err := db.Table("w_prompt").Select("slug, source_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		// First write wins — if the same slug somehow appears for multiple
		// source_ids we keep the earliest (oldest) ownership.
		if _, exists := out[r.Slug]; !exists {
			out[r.Slug] = r.SourceID
		}
	}
	return out, nil
}

// assignSlugs walks the parsed list in order and produces a slug per item.
// If a row already exists in DB for this source_id, the existing slug is
// reused so re-runs are idempotent on the (slug, lang) unique index.
// Otherwise we collision-resolve against slugs owned by other source_ids
// plus slugs already issued in this run.
func assignSlugs(parsed []parsedPrompt, slugBySourceID map[string]string, slugOwners map[string]string) []string {
	out := make([]string, len(parsed))
	taken := make(map[string]bool)
	for i, p := range parsed {
		sid := buildSourceID(p)
		// Reuse existing slug for this source_id if one is already in DB.
		if existing, ok := slugBySourceID[sid]; ok && existing != "" {
			out[i] = existing
			taken[existing] = true
			continue
		}
		base := buildSlug(p)
		s := base
		// Slug is "owned" by another source_id — collision-resolve.
		if owner, owned := slugOwners[s]; owned && owner != sid {
			suffix := p.URLBase
			if len(suffix) > 8 {
				suffix = suffix[:8]
			}
			s = base + "-" + suffix
		}
		for n := 2; taken[s] || (slugOwners[s] != "" && slugOwners[s] != sid); n++ {
			s = fmt.Sprintf("%s-%d", base, n)
		}
		taken[s] = true
		out[i] = s
	}
	return out
}

// fetchAndParse routes to the source-specific parser.
func fetchAndParse(spec *sourceSpec) ([]parsedPrompt, error) {
	switch spec.Name {
	case "sora":
		md, err := fetchURL("https://raw.githubusercontent.com/hr98w/awesome-sora-prompts/main/README.md")
		if err != nil {
			return nil, err
		}
		return parseSoraReadme(md, spec), nil
	case "veo3":
		md, err := fetchURL("https://raw.githubusercontent.com/jax-explorer/awesome-veo3-videos/main/README.md")
		if err != nil {
			return nil, err
		}
		return parseVeo3Readme(md, spec), nil
	case "kling":
		base := "https://raw.githubusercontent.com/maciejdzierzek/kling-ai-prompt-generator/main/examples"
		var all []parsedPrompt
		for _, file := range []struct{ name, promptType, sectionTag string }{
			{"text-to-video.md", "text_to_video", "text"},
			{"image-to-video.md", "image_to_video", "image"},
			{"motion-control.md", "text_to_video", "motion"},
		} {
			md, err := fetchURL(base + "/" + file.name)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", file.name, err)
			}
			all = append(all, parseKlingExamples(md, spec, file.promptType, file.sectionTag)...)
		}
		return all, nil
	case "seedance", "gpt-image-2":
		var base string
		switch spec.Name {
		case "seedance":
			base = "https://raw.githubusercontent.com/YouMind-OpenLab/awesome-seedance-2-prompts/main"
		case "gpt-image-2":
			base = "https://raw.githubusercontent.com/YouMind-OpenLab/awesome-gpt-image-2/main"
		}
		// File → simple lang code mapping. Regional variants we don't carry
		// (zh-TW, es-419, pt-PT, hi-IN) are skipped; the repos don't have
		// readme files for our remaining langs (ar, he, nl, pl, ru, sv).
		fileLangs := []struct{ file, lang string }{
			{"README.md", "en"},
			{"README_zh.md", "zh"},
			{"README_ja-JP.md", "ja"},
			{"README_ko-KR.md", "ko"},
			{"README_th-TH.md", "th"},
			{"README_vi-VN.md", "vi"},
			{"README_es-ES.md", "es"},
			{"README_de-DE.md", "de"},
			{"README_fr-FR.md", "fr"},
			{"README_it-IT.md", "it"},
			{"README_pt-BR.md", "pt"},
			{"README_tr-TR.md", "tr"},
		}
		mds := make(map[string]string, len(fileLangs))
		for _, fl := range fileLangs {
			md, err := fetchURL(base + "/" + fl.file)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", fl.file, err)
			}
			mds[fl.lang] = md
		}
		return parseYouMindReadmes(mds, spec), nil
	}
	return nil, fmt.Errorf("unknown source %q", spec.Name)
}

// fetchURL pulls a URL with a 30-second budget.
func fetchURL(u string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "workmax-importer/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// parseSoraReadme walks the hr98w README and pulls each `> ...` blockquote
// followed by a `Generated Videos: [link](URL)` closer, scoped to the three
// "Official ..." sections.
func parseSoraReadme(md string, spec *sourceSpec) []parsedPrompt {
	lines := strings.Split(md, "\n")

	headingIdx := map[string]int{}
	for i, line := range lines {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		h := strings.TrimSpace(strings.TrimPrefix(line, "## "))
		headingIdx[h] = i
	}

	type heading struct {
		text string
		line int
	}
	var allHeadings []heading
	for i, line := range lines {
		if strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "# ") {
			allHeadings = append(allHeadings, heading{
				text: strings.TrimSpace(strings.TrimLeft(line, "# ")),
				line: i,
			})
		}
	}

	type sectionWindow struct {
		def        soraSection
		start, end int
	}
	var windows []sectionWindow
	for _, sec := range soraSections {
		hLine, ok := headingIdx[sec.heading]
		if !ok {
			fmt.Fprintf(os.Stderr, "WARN: section %q not found in sora README\n", sec.heading)
			continue
		}
		end := len(lines)
		for _, h := range allHeadings {
			if h.line > hLine {
				end = h.line
				break
			}
		}
		windows = append(windows, sectionWindow{def: sec, start: hLine + 1, end: end})
	}

	linkRe := regexp.MustCompile(`Generated Videos:\s*\[link\]\(([^)]+)\)`)

	var out []parsedPrompt
	for _, w := range windows {
		var quoteBuf []string
		flush := func(videoURL string) {
			if len(quoteBuf) == 0 {
				return
			}
			content := strings.TrimSpace(strings.Join(quoteBuf, "\n"))
			if content == "" {
				quoteBuf = nil
				return
			}
			urlBase := videoURLBase(videoURL)
			if urlBase == "" {
				quoteBuf = nil
				return
			}
			out = append(out, parsedPrompt{
				Spec:        spec,
				SectionSlug: w.def.slug,
				SectionTag:  w.def.tag,
				PromptType:  "text_to_video",
				VideoURL:    videoURL,
				URLBase:     urlBase,
				Content:     content,
			})
			quoteBuf = nil
		}

		for i := w.start; i < w.end; i++ {
			line := lines[i]
			trimmed := strings.TrimRight(line, " \t\r")
			if strings.HasPrefix(trimmed, "> ") || trimmed == ">" {
				body := strings.TrimPrefix(trimmed, ">")
				body = strings.TrimPrefix(body, " ")
				quoteBuf = append(quoteBuf, body)
				continue
			}
			if m := linkRe.FindStringSubmatch(trimmed); m != nil {
				flush(strings.TrimSpace(m[1]))
				continue
			}
			if strings.TrimSpace(trimmed) == "" {
				continue
			}
			if len(quoteBuf) > 0 {
				quoteBuf = nil
			}
		}
	}
	return out
}

// parseVeo3Readme walks the jax-explorer README. Each entry is a
// `### Case N [optional title]` heading followed by `- **Source:** ...`,
// `- **Prompt:** "..."` (potentially multi-line), and a `- **Video:**`
// separator with the github user-attachments URL on the next non-empty line.
func parseVeo3Readme(md string, spec *sourceSpec) []parsedPrompt {
	lines := strings.Split(md, "\n")

	caseHeading := regexp.MustCompile(`^### Case (\d+)(?:\s+(.+))?\s*$`)
	urlRe := regexp.MustCompile(`https://github\.com/user-attachments/[^\s)]+`)

	var out []parsedPrompt
	i := 0
	for i < len(lines) {
		m := caseHeading.FindStringSubmatch(lines[i])
		if m == nil {
			i++
			continue
		}
		caseNum := m[1]
		title := strings.TrimSpace(m[2])

		// Find next case heading or EOF — defines this entry's window.
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if caseHeading.MatchString(lines[j]) {
				end = j
				break
			}
		}

		var prompt string
		var videoURL string

		// Locate "- **Prompt:**" line(s)
		for j := i + 1; j < end; j++ {
			line := lines[j]
			if strings.HasPrefix(strings.TrimSpace(line), "- **Prompt:**") {
				// Body starts after "**Prompt:**". Strip the marker, then
				// pull a quoted block — single line or multi-line until the
				// closing `"`.
				body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- **Prompt:**"))
				body = strings.TrimSpace(body)
				if strings.HasPrefix(body, "\"") {
					body = strings.TrimPrefix(body, "\"")
					if strings.HasSuffix(body, "\"") && len(body) > 0 {
						prompt = strings.TrimSuffix(body, "\"")
					} else {
						// Multi-line prompt — accumulate until the closing quote.
						var buf []string
						buf = append(buf, body)
						for k := j + 1; k < end; k++ {
							ln := lines[k]
							if strings.HasSuffix(strings.TrimRight(ln, " \t\r"), "\"") {
								buf = append(buf, strings.TrimSuffix(strings.TrimRight(ln, " \t\r"), "\""))
								break
							}
							buf = append(buf, ln)
						}
						prompt = strings.TrimSpace(strings.Join(buf, "\n"))
					}
				} else {
					prompt = body
				}
				break
			}
		}

		// Locate the github user-attachments URL.
		for j := i + 1; j < end; j++ {
			if u := urlRe.FindString(lines[j]); u != "" {
				videoURL = u
				break
			}
		}

		if prompt != "" {
			urlBase := videoURLBase(videoURL)
			if urlBase == "" {
				urlBase = "case-" + caseNum
			}
			tag := "case"
			if title != "" {
				tag = strings.ToLower(slugify(title))
				if tag == "" {
					tag = "case"
				}
			}
			out = append(out, parsedPrompt{
				Spec:        spec,
				SectionSlug: "case-" + caseNum,
				SectionTag:  tag,
				PromptType:  "text_to_video",
				VideoURL:    videoURL,
				URLBase:     urlBase,
				Content:     prompt,
			})
		}
		i = end
	}
	return out
}

// parseKlingExamples walks one of the kling-ai-prompt-generator example
// files. Each entry is a `## Section Title` heading whose body contains a
// fenced ```code block``` with the prompt itself.
func parseKlingExamples(md string, spec *sourceSpec, promptType, sectionTag string) []parsedPrompt {
	lines := strings.Split(md, "\n")
	headingRe := regexp.MustCompile(`^## (.+)\s*$`)

	var out []parsedPrompt
	i := 0
	for i < len(lines) {
		m := headingRe.FindStringSubmatch(lines[i])
		if m == nil {
			i++
			continue
		}
		title := strings.TrimSpace(m[1])

		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if headingRe.MatchString(lines[j]) {
				end = j
				break
			}
		}

		// Find the first fenced code block in this window.
		var content string
		for j := i + 1; j < end; j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
				var buf []string
				for k := j + 1; k < end; k++ {
					if strings.HasPrefix(strings.TrimSpace(lines[k]), "```") {
						break
					}
					buf = append(buf, lines[k])
				}
				content = strings.TrimSpace(strings.Join(buf, "\n"))
				break
			}
		}
		if content == "" {
			i = end
			continue
		}

		baseSlug := slugify(title)
		if baseSlug == "" {
			baseSlug = "section"
		}
		// SectionSlug stays short (just the file-type tag) so the assembled
		// `repo:section:urlbase` source_id stays under varchar(100). The
		// URLBase carries the descriptive part.
		out = append(out, parsedPrompt{
			Spec:        spec,
			SectionSlug: sectionTag,
			SectionTag:  sectionTag,
			PromptType:  promptType,
			VideoURL:    "",
			URLBase:     baseSlug,
			Content:     content,
		})
		i = end
	}
	return out
}

// parseYouMindReadmes parses each lang's README and pairs entries by
// `?id=NNN` youmind ID (the only stable identifier shared between files).
// Featured entries (## 🔥 ...) and All Prompts entries (## 🎬 / 📋 ...) are
// both captured. Each output parsedPrompt carries pre-translated fields per
// non-en language so the importer can skip LLM calls for those.
//
// Used by both YouMind-OpenLab/awesome-seedance-2-prompts (video, 🎬) and
// YouMind-OpenLab/awesome-gpt-image-2 (image, 📋).
func parseYouMindReadmes(mds map[string]string, spec *sourceSpec) []parsedPrompt {
	enMD, ok := mds["en"]
	if !ok {
		return nil
	}
	isImage := spec.Name == "gpt-image-2"
	enEntries := parseYouMindLang(enMD, "en", isImage)

	// Build per-lang index keyed by YoumindID.
	indexByLang := map[string]map[string]youmindEntry{}
	for lang, md := range mds {
		if lang == "en" {
			continue
		}
		entries := parseYouMindLang(md, lang, isImage)
		idx := make(map[string]youmindEntry, len(entries))
		for _, e := range entries {
			idx[e.YoumindID] = e
		}
		indexByLang[lang] = idx
	}

	medium := "video"
	promptType := "text_to_video"
	if isImage {
		medium = "image"
		promptType = "text_to_image"
	}

	var out []parsedPrompt
	for _, en := range enEntries {
		if en.YoumindID == "" || en.Content == "" {
			continue
		}
		section := "all"
		if en.IsFeatured {
			section = "featured"
		}

		pp := parsedPrompt{
			Spec:         spec,
			SectionSlug:  section,
			SectionTag:   section,
			PromptType:   promptType,
			Medium:       medium,
			URLBase:      en.YoumindID,
			Content:      en.Content,
			Title:        en.Title,
			Description:  en.Description,
			Translations: map[string]localizedFields{},
		}
		if isImage {
			pp.ImageURL = en.AssetURL
		} else {
			videoURL := en.AssetURL
			if videoURL == "" {
				videoURL = fmt.Sprintf(
					"https://github.com/YouMind-OpenLab/awesome-seedance-2-prompts/releases/download/videos/%s.mp4",
					en.YoumindID,
				)
			}
			pp.VideoURL = videoURL
		}
		for lang, idx := range indexByLang {
			if e, ok := idx[en.YoumindID]; ok && e.Content != "" {
				pp.Translations[lang] = localizedFields{
					Title:       e.Title,
					Content:     e.Content,
					Description: e.Description,
				}
			}
		}
		out = append(out, pp)
	}
	return out
}

// youmindEntry is the lang-agnostic intermediate from one README walk.
type youmindEntry struct {
	YoumindID   string
	IsFeatured  bool
	Title       string
	Content     string
	Description string
	AssetURL    string // direct mp4 (seedance) or image URL (gpt-image-2)
}

// parseYouMindLang walks one localized README and extracts entries from the
// Featured + All Prompts sections.
//
// Section headings are localized in every README, but they consistently
// start with `## 🔥 ` (Featured) and either `## 🎬 ` (seedance video) or
// `## 📋 ` (gpt-image-2 image) for All Prompts. Detecting by emoji prefix
// is more robust than hardcoding 16 lang-specific translations.
func parseYouMindLang(md, lang string, isImage bool) []youmindEntry {
	lines := strings.Split(md, "\n")

	// Find section line bounds by emoji prefix — language-independent.
	// All Prompts heading uses `🎬` in seedance, `📋` in gpt-image-2.
	featStart, featEnd := -1, -1
	allStart, allEnd := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		if !strings.HasPrefix(trimmed, "## ") {
			continue
		}
		isAllHeading := strings.HasPrefix(trimmed, "## 🎬 ") || strings.HasPrefix(trimmed, "## 📋 ")
		switch {
		case featStart < 0 && strings.HasPrefix(trimmed, "## 🔥 "):
			featStart = i + 1
		case allStart < 0 && isAllHeading:
			if featStart >= 0 && featEnd < 0 {
				featEnd = i
			}
			allStart = i + 1
		default:
			// Any later `## ` heading closes whichever section is open.
			if allStart >= 0 && allEnd < 0 {
				allEnd = i
			} else if featStart >= 0 && featEnd < 0 {
				featEnd = i
			}
		}
	}
	if featEnd < 0 {
		featEnd = len(lines)
	}
	if allEnd < 0 {
		allEnd = len(lines)
	}

	var out []youmindEntry
	if featStart >= 0 {
		out = append(out, parseYouMindWindow(lines[featStart:featEnd], true, isImage)...)
	}
	if allStart >= 0 {
		out = append(out, parseYouMindWindow(lines[allStart:allEnd], false, isImage)...)
	}
	return out
}

var youmindIDRe = regexp.MustCompile(`\?id=(\d+)`)
var seedanceMP4Re = regexp.MustCompile(`https://github\.com/YouMind-OpenLab/awesome-seedance-2-prompts/releases/download/videos/\d+\.mp4`)
var youmindImgRe = regexp.MustCompile(`<img[^>]*src="(https://[^"\s]+\.(?:jpg|jpeg|png|webp))"`)

// parseYouMindWindow walks a slice of README lines (Featured or All) and
// extracts each `### ...` entry's metadata: title, ?id=NNN, fenced prompt,
// optional `> ...` blockquote description, and the asset URL — direct mp4
// for seedance, or first image src for gpt-image-2.
func parseYouMindWindow(lines []string, featured, isImage bool) []youmindEntry {
	var out []youmindEntry

	// Find each `### ` heading position to define entry windows.
	var entryStarts []int
	for i, line := range lines {
		if strings.HasPrefix(line, "### ") {
			entryStarts = append(entryStarts, i)
		}
	}

	for k, start := range entryStarts {
		end := len(lines)
		if k+1 < len(entryStarts) {
			end = entryStarts[k+1]
		}

		// Extract title from heading line. Some entries are `No. N: <title>`
		// (always for Featured; for gpt-image-2 also for All Prompts), some
		// are bare `<title>` (seedance All Prompts). Strip the `No. N:`
		// prefix whenever present.
		raw := strings.TrimSpace(strings.TrimPrefix(lines[start], "### "))
		title := raw
		if strings.HasPrefix(raw, "No. ") {
			if i := strings.Index(raw, ": "); i >= 0 {
				title = strings.TrimSpace(raw[i+2:])
			}
		}

		var description string
		var content string
		var youmindID string
		var assetURL string

		// Walk the entry body.
		i := start + 1
		for i < end {
			line := lines[i]
			trimmed := strings.TrimRight(line, " \t\r")

			// Blockquote one-liner above the prompt → description.
			if strings.HasPrefix(trimmed, "> ") && description == "" {
				description = strings.TrimSpace(strings.TrimPrefix(trimmed, "> "))
				i++
				continue
			}

			// Fenced prompt block — capture between ``` markers.
			if strings.HasPrefix(strings.TrimSpace(trimmed), "```") && content == "" {
				var buf []string
				j := i + 1
				for j < end {
					if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
						break
					}
					buf = append(buf, lines[j])
					j++
				}
				content = strings.TrimSpace(strings.Join(buf, "\n"))
				i = j + 1
				continue
			}

			// Look for `?id=NNN` on any line — canonical pairing key.
			if youmindID == "" {
				if m := youmindIDRe.FindStringSubmatch(trimmed); m != nil {
					youmindID = m[1]
				}
			}

			// Asset URL: mp4 (seedance) or first image src (gpt-image-2).
			if assetURL == "" {
				if isImage {
					if m := youmindImgRe.FindStringSubmatch(trimmed); m != nil {
						assetURL = m[1]
					}
				} else if u := seedanceMP4Re.FindString(trimmed); u != "" {
					assetURL = u
				}
			}

			i++
		}

		// "Description" sometimes lives under a `#### 📖 Description` heading
		// instead of as a blockquote. Scrape the paragraph that follows.
		if description == "" {
			for i := start + 1; i < end-1; i++ {
				t := strings.TrimSpace(lines[i])
				if t == "#### 📖 Description" || t == "#### 📖 描述" {
					// Take the next non-blank line as the description.
					for j := i + 1; j < end; j++ {
						if s := strings.TrimSpace(lines[j]); s != "" && !strings.HasPrefix(s, "#") {
							description = s
							break
						}
					}
					break
				}
			}
		}

		if content == "" || youmindID == "" {
			continue
		}
		out = append(out, youmindEntry{
			YoumindID:   youmindID,
			IsFeatured:  featured,
			Title:       title,
			Content:     content,
			Description: description,
			AssetURL:    assetURL,
		})
	}
	return out
}

// slugify converts free text into a URL-safe ASCII slug.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.Trim(nonAlnum.ReplaceAllString(s, "-"), "-")
}

// videoURLBase reduces a URL to a stable identifier suitable for source_id.
// Examples:
//
//	https://cdn.openai.com/sora/videos/tokyo-walk.mp4 -> tokyo-walk
//	https://x.com/_tim_brooks/status/1761236971186438178?s=20 -> 1761236971186438178
//	https://github.com/user-attachments/assets/ef27...3741c -> ef27...3741c
func videoURLBase(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return ""
	}
	base := path.Base(u.Path)
	if idx := strings.LastIndex(base, "."); idx > 0 {
		base = base[:idx]
	}
	return strings.ToLower(base)
}

// buildSlug derives a stable URL slug, prefixed with the source spec's
// SlugPrefix (e.g. `sora-...`, `veo3-...`, `kling-i2v-...`).
func buildSlug(p parsedPrompt) string {
	prefix := p.Spec.SlugPrefix
	if p.PromptType == "image_to_video" {
		prefix = prefix + "-i2v"
	}
	words := slugWords(p.Content, 6)
	if len(words) == 0 {
		return prefix + "-" + p.URLBase
	}
	return prefix + "-" + strings.Join(words, "-")
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true,
	"is": true, "are": true, "was": true, "were": true, "be": true, "been": true,
	"of": true, "in": true, "on": true, "at": true, "to": true, "for": true,
	"with": true, "by": true, "from": true, "as": true, "into": true, "onto": true,
	"this": true, "that": true, "these": true, "those": true, "it": true, "its": true,
}

func slugWords(content string, n int) []string {
	lower := strings.ToLower(content)
	lower = regexp.MustCompile(`^\s*\d+\.\s*`).ReplaceAllString(lower, "")
	tokens := nonAlnum.Split(lower, -1)
	out := make([]string, 0, n)
	for _, t := range tokens {
		if t == "" || stopWords[t] {
			continue
		}
		if len(t) <= 1 && len(out) > 0 {
			continue
		}
		out = append(out, t)
		if len(out) >= n {
			break
		}
	}
	return out
}

type plannedItem struct {
	parsed    parsedPrompt
	sourceID  string
	slug      string
	needLangs []string // e.g. ["en", "zh", "ja", ...]
}

func processOne(
	db *gorm.DB,
	llmSvc *llmService.UniversalLLMService,
	pl plannedItem,
	sortValue, statusValue int,
	force, skipTranslate bool,
) (int, error) {
	en := buildEnglishRow(pl, sortValue, statusValue)

	// Phase 1: collect all rows that don't require an LLM call (en plus any
	// langs the parser pre-translated). These get written in a single
	// batched INSERT to amortize network round-trips.
	var batch []*model.Prompt
	var llmLangs []string
	for _, lang := range pl.needLangs {
		switch {
		case lang == "en":
			batch = append(batch, en)
		case pl.parsed.hasTranslation(lang):
			batch = append(batch, buildRowFromTranslation(pl, en, lang))
		default:
			if !skipTranslate {
				llmLangs = append(llmLangs, lang)
			}
		}
	}

	writes := 0
	if len(batch) > 0 {
		if err := upsertPromptBatch(db, batch, force); err != nil {
			return writes, fmt.Errorf("batch write: %w", err)
		}
		writes += len(batch)
	}

	// Phase 2: LLM-translated langs are sequential — each one is an
	// independent network call and a single-row upsert.
	for _, lang := range llmLangs {
		row, err := translateTo(llmSvc, en, lang)
		if err != nil {
			return writes, fmt.Errorf("translate %s: %w", lang, err)
		}
		if err := upsertPrompt(db, row, force); err != nil {
			return writes, fmt.Errorf("write %s: %w", lang, err)
		}
		writes++
	}
	return writes, nil
}

// upsertPromptBatch issues a single INSERT with multiple VALUES rows,
// using ON DUPLICATE KEY UPDATE / INSERT IGNORE depending on `force`.
// Collapses N sequential round-trips into one.
func upsertPromptBatch(db *gorm.DB, rows []*model.Prompt, force bool) error {
	if len(rows) == 0 {
		return nil
	}
	var onConflict clause.OnConflict
	if force {
		onConflict = clause.OnConflict{
			DoUpdates: clause.AssignmentColumns([]string{
				"slug", "title", "prompt_content", "prompt_preview", "prompt_description",
				"medium", "prompt_type", "image_width", "image_height", "aspect_ratio",
				"preview_asset_type", "preview_video", "supported_models", "primary_model",
				"model_params", "category", "tags", "style",
				"seo_title", "seo_keyword", "seo_description",
				"status", "sort", "content_hash", "last_sync_at", "search_vector",
			}),
		}
	} else {
		onConflict = clause.OnConflict{DoNothing: true}
	}
	return db.Clauses(onConflict).Create(&rows).Error
}

// buildRowFromTranslation produces a non-en row by copying the en row's
// metadata and overlaying the source-provided translated fields.
func buildRowFromTranslation(pl plannedItem, en *model.Prompt, lang string) *model.Prompt {
	t := pl.parsed.Translations[lang]
	out := *en
	out.Id = 0
	out.CreatedAt = time.Time{}
	out.UpdatedAt = time.Time{}
	out.Lang = lang
	if title := strings.TrimSpace(t.Title); title != "" {
		out.Title = title
	}
	out.PromptContent = t.Content
	out.PromptPreview = truncate(t.Content, 180)
	if d := strings.TrimSpace(t.Description); d != "" {
		out.PromptDescription = d
		out.SeoDescription = d
	}
	// Rebuild seo_title from the (possibly translated) title.
	out.SeoTitle = out.Title + " " + seoSuffixForLang(lang)
	out.UpdateSearchVector()
	return &out
}

// seoSuffixForLang returns the locale-appropriate "Video Prompt" suffix.
func seoSuffixForLang(lang string) string {
	switch lang {
	case "zh":
		return "视频提示词"
	case "ja":
		return "ビデオプロンプト"
	case "ko":
		return "비디오 프롬프트"
	case "th":
		return "พร้อมต์วิดีโอ"
	case "vi":
		return "Lời nhắc Video"
	case "es":
		return "Prompt de Video"
	case "de":
		return "Video-Prompt"
	case "fr":
		return "Invite Vidéo"
	case "it":
		return "Prompt Video"
	case "pt":
		return "Prompt de Vídeo"
	case "tr":
		return "Video Komutu"
	case "ar":
		return "محث الفيديو"
	case "he":
		return "הנחיית וידאו"
	case "nl":
		return "Video-Prompt"
	case "pl":
		return "Polecenie Wideo"
	case "ru":
		return "Видеоподсказка"
	case "sv":
		return "Videoprompt"
	default:
		return "Video Prompt"
	}
}

func buildEnglishRow(pl plannedItem, sortValue, statusValue int) *model.Prompt {
	spec := pl.parsed.Spec

	supModelsJSON, _ := json.Marshal(spec.SupportedModels)

	tags := append([]string{}, spec.BaseTags...)
	if pl.parsed.SectionTag != "" {
		tags = append(tags, pl.parsed.SectionTag)
	}
	tagsJSON, _ := json.Marshal(tags)

	title := strings.TrimSpace(pl.parsed.Title)
	if title == "" {
		title = titleFromContent(pl.parsed.Content)
	}
	preview := truncate(pl.parsed.Content, 180)
	desc := strings.TrimSpace(pl.parsed.Description)
	if desc == "" {
		desc = fmt.Sprintf(spec.DescTemplate, pl.parsed.SectionTag)
	}

	now := time.Now()
	hash := contentHash(pl.parsed.Content + "|" + pl.parsed.VideoURL)

	medium := pl.parsed.Medium
	if medium == "" {
		medium = "video"
	}

	previewVideo := ""
	previewType := "image"
	coverImage := ""
	if medium == "image" {
		// Image source — store the asset URL as cover/local/thumb so the
		// chip-strip frontend's pickFirstNonEmpty(thumb,local,cover) picks it up.
		coverImage = pl.parsed.ImageURL
	} else if isDirectVideo(pl.parsed.VideoURL) {
		previewVideo = pl.parsed.VideoURL
		previewType = "video"
	}

	seoSuffix := "Video Prompt"
	if medium == "image" {
		seoSuffix = "Image Prompt"
	}

	row := &model.Prompt{
		SourceType:        "github",
		SourceID:          pl.sourceID,
		Slug:              pl.slug,
		Title:             title,
		Lang:              "en",
		PromptContent:     pl.parsed.Content,
		PromptPreview:     preview,
		PromptDescription: desc,
		Medium:            medium,
		PromptType:        pl.parsed.PromptType,
		CoverImage:        coverImage,
		LocalImage:        coverImage,
		ThumbImage:        coverImage,
		ImageWidth:        spec.Width,
		ImageHeight:       spec.Height,
		AspectRatio:       spec.AspectRatio,
		PreviewAssetType:  previewType,
		PreviewVideo:      previewVideo,
		SupportedModels:   string(supModelsJSON),
		PrimaryModel:      spec.PrimaryModel,
		ModelParams:       spec.ModelParams,
		Category:          "general",
		Tags:              string(tagsJSON),
		Style:             spec.Style,
		SeoTitle:          title + " " + seoSuffix,
		SeoKeyword:        strings.Join([]string{spec.PrimaryModel + " " + strings.ToLower(seoSuffix), title + " prompt", spec.Name + " prompt"}, ", "),
		SeoDescription:    desc,
		Status:            statusValue,
		Sort:              isFeaturedSort(pl.parsed.SectionTag, sortValue),
		IsFeatured:        pl.parsed.SectionTag == "featured",
		IsTrending:        false,
		ContentHash:       hash,
		CollectedAt:       now,
		LastSyncAt:        now,
	}
	row.UpdateSearchVector()
	return row
}

func titleFromContent(content string) string {
	first := content
	if i := strings.IndexAny(first, ".!?\n"); i > 0 {
		first = first[:i]
	}
	first = regexp.MustCompile(`^\s*\d+\.\s*`).ReplaceAllString(first, "")
	first = strings.TrimSpace(first)
	if first == "" {
		first = content
	}
	words := strings.Fields(first)
	const maxWords = 10
	if len(words) > maxWords {
		first = strings.Join(words[:maxWords], " ")
	}
	parts := strings.Fields(first)
	for i, w := range parts {
		lw := strings.ToLower(w)
		if i > 0 && stopWords[lw] {
			parts[i] = lw
			continue
		}
		runes := []rune(w)
		if len(runes) > 0 {
			runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
		}
		parts[i] = string(runes)
	}
	return strings.Join(parts, " ")
}

// isFeaturedSort gives upstream-Featured imports a higher sort weight so
// they outrank the bulk auto-imports in the featured chip strip.
func isFeaturedSort(sectionTag string, defaultSort int) int {
	if sectionTag == "featured" {
		return 30
	}
	return defaultSort
}

func isDirectVideo(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	pathLower := strings.ToLower(u.Path)
	// CDN-hosted mp4/webm/mov on trusted hosts.
	if strings.HasSuffix(pathLower, ".mp4") || strings.HasSuffix(pathLower, ".webm") || strings.HasSuffix(pathLower, ".mov") {
		switch host {
		case "cdn.openai.com", "cdn.openai.org", "videos.openai.com":
			return true
		}
	}
	// github.com URLs serve mp4 inline both for user-attachments (jax-explorer)
	// and for /releases/download/.../*.mp4 (YouMind-OpenLab seedance repo).
	if host == "github.com" && strings.HasSuffix(pathLower, ".mp4") {
		if strings.HasPrefix(u.Path, "/user-attachments/") || strings.Contains(u.Path, "/releases/download/") {
			return true
		}
	}
	return false
}

func contentHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

// upsertPrompt writes a row with a single round-trip using
// INSERT ... ON DUPLICATE KEY UPDATE on the (source_id, lang) unique index.
// Without --force, conflicts are silently ignored — matches the prior
// "skip if exists" semantics. With --force, mutable canonical fields are
// refreshed but counters (view_count, copy_count, ...) and timestamps that
// shouldn't regress are preserved by leaving them out of the update set.
func upsertPrompt(db *gorm.DB, row *model.Prompt, force bool) error {
	var onConflict clause.OnConflict
	if force {
		// MySQL: INSERT ... ON DUPLICATE KEY UPDATE col = VALUES(col), ...
		onConflict = clause.OnConflict{
			DoUpdates: clause.AssignmentColumns([]string{
				"slug", "title", "prompt_content", "prompt_preview", "prompt_description",
				"medium", "prompt_type", "image_width", "image_height", "aspect_ratio",
				"preview_asset_type", "preview_video", "supported_models", "primary_model",
				"model_params", "category", "tags", "style",
				"seo_title", "seo_keyword", "seo_description",
				"status", "sort", "content_hash", "last_sync_at", "search_vector",
			}),
		}
	} else {
		// MySQL: INSERT IGNORE — silently skip rows that conflict on any unique key.
		onConflict = clause.OnConflict{DoNothing: true}
	}
	return db.Clauses(onConflict).Create(row).Error
}

// translateTo asks the LLM to translate the en row into the named language.
// Used as fallback when the source repo doesn't ship a translation file for
// that lang.
func translateTo(llmSvc *llmService.UniversalLLMService, en *model.Prompt, lang string) (*model.Prompt, error) {
	target, ok := langDisplayName[lang]
	if !ok {
		return nil, fmt.Errorf("unknown lang code %q", lang)
	}

	systemPrompt := fmt.Sprintf(`You are a professional translator specializing in AI video generation prompts.

Translate the provided JSON object's string fields from English to %s.

Rules:
1. Preserve meaning and structure. The translation must read naturally in the target language.
2. Keep all model names, brand names, and English technical terms (Sora, Veo, Kling, Seedance, "1:1", "16:9", camera lens specs) untranslated.
3. Preserve line breaks within string values.
4. Output a JSON object with the SAME keys: title, prompt_content, prompt_description, seo_title, seo_keyword, seo_description.
5. No markdown fences, no commentary — return ONLY the JSON.`, target)

	payload := map[string]string{
		"title":              en.Title,
		"prompt_content":     en.PromptContent,
		"prompt_description": en.PromptDescription,
		"seo_title":          en.SeoTitle,
		"seo_keyword":        en.SeoKeyword,
		"seo_description":    en.SeoDescription,
	}
	payloadJSON, _ := json.Marshal(payload)

	// Long seedance prompts (~3KB) routinely take 60-90s on deepseek; 180s
	// keeps the long-tail in budget without leaving worker pools idle.
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	resp, err := llmSvc.ProcessRequest(ctx, llmService.UniversalLLMRequest{
		SystemPrompt: systemPrompt,
		UserMessage:  string(payloadJSON),
		Temperature:  0.2,
		MaxTokens:    4000,
		Service:      "import_video_prompts",
		RequestID:    fmt.Sprintf("import_%s_%s", lang, en.Slug),
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Content == "" {
		return nil, fmt.Errorf("empty translation response")
	}

	cleaned := stripCodeFences(strings.TrimSpace(resp.Content))
	var translated map[string]string
	if err := json.Unmarshal([]byte(cleaned), &translated); err != nil {
		return nil, fmt.Errorf("parse translation json: %w (raw: %.200s)", err, cleaned)
	}

	out := *en
	out.Id = 0
	out.CreatedAt = time.Time{}
	out.UpdatedAt = time.Time{}
	out.Lang = lang
	out.Title = pick(translated["title"], en.Title)
	out.PromptContent = pick(translated["prompt_content"], en.PromptContent)
	out.PromptDescription = pick(translated["prompt_description"], en.PromptDescription)
	out.PromptPreview = truncate(out.PromptContent, 180)
	out.SeoTitle = pick(translated["seo_title"], en.SeoTitle)
	out.SeoKeyword = pick(translated["seo_keyword"], en.SeoKeyword)
	out.SeoDescription = pick(translated["seo_description"], en.SeoDescription)
	out.UpdateSearchVector()
	return &out, nil
}

// parseLangsFlag parses the --langs flag value. `all` expands to the full
// supportedTargetLangs list; otherwise the value is a comma-separated list
// of lang codes.
func parseLangsFlag(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if raw == "all" {
		out := make([]string, len(supportedTargetLangs))
		copy(out, supportedTargetLangs)
		return out
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		l := strings.TrimSpace(p)
		if l == "" || seen[l] {
			continue
		}
		if _, ok := langDisplayName[l]; !ok {
			fmt.Fprintf(os.Stderr, "WARN: unknown lang code %q — skipping\n", l)
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

func pick(translated, fallback string) string {
	t := strings.TrimSpace(translated)
	if t == "" {
		return fallback
	}
	return t
}

func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx >= 0 {
			s = s[idx+1:]
		}
		if strings.HasSuffix(s, "```") {
			s = strings.TrimSuffix(s, "```")
		}
		s = strings.TrimSpace(s)
	}
	return s
}
