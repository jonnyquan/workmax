// retranslate_prompts re-runs the translation pipeline for prompts that
// were quarantined to status=2 because their prompt_content still contained
// Chinese characters even though lang <> 'zh'. For each quarantined row it
// pulls the corresponding zh source row (same slug), asks the configured
// text LLM to translate it into the target language, writes back the
// translated content, and flips status back to 1.
//
// Usage:
//   cd server && go run ./cmd/retranslate_prompts                 # all quarantined
//   cd server && go run ./cmd/retranslate_prompts --lang=ja        # only ja
//   cd server && go run ./cmd/retranslate_prompts --limit=20       # smoke test
//   cd server && go run ./cmd/retranslate_prompts --dry-run        # plan only
//   cd server && go run ./cmd/retranslate_prompts --workers=8      # concurrency
//
// Idempotent: only touches rows where status=2 AND lang<>'zh' AND
// prompt_content REGEXP '[一-龥]{3,}'. After translation the row's
// prompt_content is replaced and status set to 1 — so re-runs only pick
// up rows that still match the quarantine criteria.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"server/config"
	"server/globals"
	llmService "server/service/llm"

	"github.com/spf13/viper"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

var langDisplayName = map[string]string{
	"en": "English",
	"de": "German",
	"fr": "French",
	"es": "Spanish",
	"it": "Italian",
	"pt": "Portuguese",
	"ru": "Russian",
	"ja": "Japanese",
	"ko": "Korean",
	"ar": "Arabic",
	"he": "Hebrew",
	"hi": "Hindi",
	"nl": "Dutch",
	"sv": "Swedish",
	"pl": "Polish",
	"tr": "Turkish",
	"th": "Thai",
	"vi": "Vietnamese",
}

var cnRegex = regexp.MustCompile(`\p{Han}{3,}`)

// hasJapaneseKana returns true if the string contains hiragana or katakana —
// a robust positive signal that text is Japanese rather than Chinese.
func hasJapaneseKana(s string) bool {
	for _, r := range s {
		// Hiragana 3040-309F or Katakana 30A0-30FF
		if (r >= 0x3040 && r <= 0x309F) || (r >= 0x30A0 && r <= 0x30FF) {
			return true
		}
	}
	return false
}

// hanDensity reports the ratio of Han characters to total non-whitespace runes.
func hanDensity(s string) float64 {
	var total, han int
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			continue
		case (r >= 0x4e00 && r <= 0x9fff) || (r >= 0x3400 && r <= 0x4dbf):
			han++
			total++
		default:
			total++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(han) / float64(total)
}

type quarantinedRow struct {
	ID            uint   `gorm:"column:id"`
	Slug          string `gorm:"column:slug"`
	Lang          string `gorm:"column:lang"`
	PromptContent string `gorm:"column:prompt_content"`
}

func (quarantinedRow) TableName() string { return "w_prompt" }

type sourceRow struct {
	PromptContent string `gorm:"column:prompt_content"`
}

func (sourceRow) TableName() string { return "w_prompt" }

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	langFilter := flag.String("lang", "", "limit to a single target lang code (e.g. 'ja')")
	limit := flag.Int("limit", 0, "stop after N rows (0 = all)")
	workers := flag.Int("workers", 4, "concurrent translation workers")
	dryRun := flag.Bool("dry-run", false, "report scope without calling LLM or writing")
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

	// Wire globals so llm package can read aihub config + log
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

	// Fetch quarantined rows
	var rows []quarantinedRow
	q := db.Table("w_prompt").
		Where("status = 2 AND lang <> 'zh' AND prompt_content REGEXP '[一-龥]{3,}'")
	if *langFilter != "" {
		q = q.Where("lang = ?", *langFilter)
	}
	if *limit > 0 {
		q = q.Limit(*limit)
	}
	if err := q.Order("lang, id").Find(&rows).Error; err != nil {
		fmt.Fprintf(os.Stderr, "fetch quarantined rows: %v\n", err)
		os.Exit(1)
	}

	if len(rows) == 0 {
		fmt.Println("Nothing to retranslate. Exiting.")
		return
	}

	// Plan summary
	byLang := map[string]int{}
	for _, r := range rows {
		byLang[r.Lang]++
	}
	fmt.Printf("Quarantined rows to translate: %d\n", len(rows))
	for lang, cnt := range byLang {
		fmt.Printf("  %s: %d\n", lang, cnt)
	}

	if *dryRun {
		fmt.Println("\nDry-run complete — no LLM calls, no writes.")
		return
	}

	if !llmService.IsClientReady() {
		fmt.Fprintln(os.Stderr, "LLM client not ready — check aihub.text config (endpoint/api_key/model + enabled=true)")
		os.Exit(1)
	}
	llmSvc := llmService.GetService()

	// Worker pool
	type job struct{ row quarantinedRow }
	type result struct {
		id      uint
		success bool
		err     error
		preview string
	}

	jobs := make(chan job, *workers*2)
	results := make(chan result, *workers*2)
	var wg sync.WaitGroup

	var done atomic.Int64
	var failed atomic.Int64

	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for j := range jobs {
				res := translateOne(db, llmSvc, j.row)
				if res.success {
					done.Add(1)
				} else {
					failed.Add(1)
				}
				results <- res
			}
		}(w)
	}

	go func() {
		for i, r := range rows {
			jobs <- job{row: r}
			if i > 0 && i%50 == 0 {
				fmt.Printf("  enqueued %d/%d\n", i, len(rows))
			}
		}
		close(jobs)
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	processed := 0
	startedAt := time.Now()
	for res := range results {
		processed++
		status := "OK "
		if !res.success {
			status = "ERR"
		}
		preview := res.preview
		if len(preview) > 60 {
			preview = preview[:60] + "..."
		}
		errMsg := ""
		if res.err != nil {
			errMsg = " — " + res.err.Error()
		}
		fmt.Printf("[%s] id=%d %s%s\n", status, res.id, preview, errMsg)
		if processed%25 == 0 || processed == len(rows) {
			elapsed := time.Since(startedAt).Seconds()
			rate := float64(processed) / elapsed
			fmt.Printf("  progress: %d/%d ok=%d failed=%d %.1f rows/s\n",
				processed, len(rows), done.Load(), failed.Load(), rate)
		}
	}

	fmt.Printf("\nDone. ok=%d failed=%d total=%d elapsed=%s\n",
		done.Load(), failed.Load(), len(rows), time.Since(startedAt).Round(time.Second))

	if failed.Load() > 0 {
		os.Exit(2)
	}
}

func translateOne(db *gorm.DB, llmSvc *llmService.UniversalLLMService, row quarantinedRow) (res struct {
	id      uint
	success bool
	err     error
	preview string
}) {
	res.id = row.ID

	// Pull zh source by slug
	var src sourceRow
	if err := db.Table("w_prompt").
		Select("prompt_content").
		Where("slug = ? AND lang = 'zh'", row.Slug).
		Limit(1).
		Scan(&src).Error; err != nil {
		res.err = fmt.Errorf("lookup zh source: %w", err)
		return
	}
	if src.PromptContent == "" {
		// fallback to the existing (CN-leaked) content as the source
		src.PromptContent = row.PromptContent
	}

	target := langDisplayName[row.Lang]
	if target == "" {
		target = strings.ToUpper(row.Lang)
	}

	systemPrompt := `You are a professional translator specializing in AI image and video generation prompts.

Rules:
1. Translate only to the requested target language.
2. Preserve meaning and structure exactly.
3. Preserve line breaks, paragraph boundaries, list markers, punctuation style, and spacing layout.
4. Keep all model parameters, ratios, and English technical terms (Lora, ControlNet, "1:1", "16:9", model names, etc.) unchanged.
5. Output only translated text. No JSON, no markdown fences, no explanations.`

	userMessage := fmt.Sprintf("Target language: %s\n\nTranslate the following source prompt to the target language while preserving original formatting exactly.\n\n<source_prompt>\n%s\n</source_prompt>",
		target, src.PromptContent)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	llmReq := llmService.UniversalLLMRequest{
		SystemPrompt: systemPrompt,
		UserMessage:  userMessage,
		Temperature:  0.2,
		MaxTokens:    4000,
		Service:      "retranslate_prompts",
		RequestID:    fmt.Sprintf("retranslate_%d", row.ID),
	}

	resp, err := llmSvc.ProcessRequest(ctx, llmReq)
	if err != nil {
		res.err = fmt.Errorf("llm: %w", err)
		return
	}
	if resp == nil || resp.Content == "" {
		res.err = fmt.Errorf("empty translation response")
		return
	}

	translated := strings.TrimSpace(resp.Content)
	// Strip surrounding code fences if the model added them despite instructions
	translated = stripCodeFences(translated)

	// Sanity-check: reject only if Han chars dominate AND we have no positive
	// signal for the target language. Japanese legitimately uses kanji (Han)
	// alongside hiragana/katakana — a ja translation with 50% kanji is normal,
	// but it must show hiragana/katakana to prove it's actually Japanese.
	// For other langs, allow up to 35% Han for IP/brand/placeholder leakage.
	if row.Lang == "ja" {
		if !hasJapaneseKana(translated) {
			res.err = fmt.Errorf("ja translation lacks hiragana/katakana signal")
			res.preview = previewLine(translated)
			return
		}
	} else if hanDensity(translated) > 0.35 {
		res.err = fmt.Errorf("translation still dominated by Chinese (density=%.2f)", hanDensity(translated))
		res.preview = previewLine(translated)
		return
	}

	// Write back + restore status=1
	if err := db.Table("w_prompt").
		Where("id = ?", row.ID).
		Updates(map[string]any{
			"prompt_content": translated,
			"status":         1,
		}).Error; err != nil {
		res.err = fmt.Errorf("update: %w", err)
		return
	}

	res.success = true
	res.preview = previewLine(translated)
	return
}

func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// drop first line
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

func previewLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return s
}
