// expand_translations creates missing language rows for prompts that
// currently only have a subset of the 18 supported languages. For each
// (slug, missing_lang) pair, it clones the en (preferred) or zh source
// row, asks the LLM to translate title + prompt_content, and inserts
// a new row with main_prompt_id pointing to the en row.
//
// Targets video prompts that only have en+zh (22 slugs × 16 missing
// langs = 352 new rows by default).
//
// Usage:
//   cd server && go run ./cmd/expand_translations --dry-run
//   cd server && go run ./cmd/expand_translations
//   cd server && go run ./cmd/expand_translations --slug=sora-atlantis-new-york
//   cd server && go run ./cmd/expand_translations --workers=8
//
// Idempotent: only inserts rows that don't yet exist for (slug, lang).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"server/config"
	"server/globals"
	"server/model"
	llmService "server/service/llm"

	"github.com/spf13/viper"
	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

var allLangs = []string{
	"en", "zh", "de", "fr", "es", "it", "pt", "ru", "ja", "ko", "ar", "he", "nl", "sv", "pl", "tr", "th", "vi",
}

var langDisplayName = map[string]string{
	"en": "English", "zh": "Simplified Chinese",
	"de": "German", "fr": "French", "es": "Spanish", "it": "Italian",
	"pt": "Portuguese", "ru": "Russian", "ja": "Japanese", "ko": "Korean",
	"ar": "Arabic", "he": "Hebrew", "nl": "Dutch", "sv": "Swedish",
	"pl": "Polish", "tr": "Turkish", "th": "Thai", "vi": "Vietnamese",
}

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml")
	slugFilter := flag.String("slug", "", "limit to a single slug")
	prefixFilter := flag.String("prefix", "", "comma-separated slug prefixes to include (e.g. 'sora-,veo-')")
	workers := flag.Int("workers", 4, "concurrent translation workers")
	dryRun := flag.Bool("dry-run", false, "report scope without writing")
	flag.Parse()

	var prefixes []string
	if *prefixFilter != "" {
		for _, p := range strings.Split(*prefixFilter, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				prefixes = append(prefixes, p)
			}
		}
	}

	v := viper.New()
	v.SetConfigFile(*cfgPath)
	if err := v.ReadInConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "read config: %v\n", err)
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
	db, err := gorm.Open(mysql.Open(m.Dsn()), &gorm.Config{
		Logger: gormLogger.Default.LogMode(gormLogger.Warn),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}

	// 1. Find slugs missing langs
	type slugInfo struct {
		Slug      string
		ExistLang string // comma-joined existing langs (subset of allLangs)
	}
	var rows []struct {
		Slug      string `gorm:"column:slug"`
		ExistLang string `gorm:"column:exist_lang"`
	}
	q := db.Table("w_prompt").
		Select("slug, GROUP_CONCAT(DISTINCT lang ORDER BY lang) AS exist_lang").
		Where("status IN (1, 2)").
		Group("slug").
		Having("COUNT(DISTINCT lang) < ?", len(allLangs))
	if *slugFilter != "" {
		q = q.Where("slug = ?", *slugFilter)
	}
	if err := q.Find(&rows).Error; err != nil {
		fmt.Fprintf(os.Stderr, "fetch slugs: %v\n", err)
		os.Exit(1)
	}

	// 2. For each, work out missing langs
	type job struct {
		slug    string
		missing []string
	}
	var jobs []job
	for _, r := range rows {
		// Optional prefix filter
		if len(prefixes) > 0 {
			matched := false
			for _, p := range prefixes {
				if strings.HasPrefix(r.Slug, p) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		have := map[string]bool{}
		for _, l := range strings.Split(r.ExistLang, ",") {
			have[l] = true
		}
		var miss []string
		for _, l := range allLangs {
			if !have[l] {
				miss = append(miss, l)
			}
		}
		if !have["en"] && !have["zh"] {
			continue
		}
		if len(miss) == 0 {
			continue
		}
		jobs = append(jobs, job{slug: r.Slug, missing: miss})
	}

	totalNew := 0
	for _, j := range jobs {
		totalNew += len(j.missing)
	}
	fmt.Printf("Slugs needing expansion: %d\n", len(jobs))
	fmt.Printf("Total rows to create:    %d\n", totalNew)
	if *dryRun {
		for i, j := range jobs {
			if i >= 10 {
				fmt.Printf("  ... +%d more\n", len(jobs)-10)
				break
			}
			fmt.Printf("  %s -> missing %v\n", j.slug, j.missing)
		}
		fmt.Println("Dry-run complete.")
		return
	}

	if !llmService.IsClientReady() {
		fmt.Fprintln(os.Stderr, "LLM client not ready")
		os.Exit(1)
	}
	llmSvc := llmService.GetService()

	type pair struct {
		slug string
		lang string
	}
	pairs := make(chan pair, *workers*2)

	var ok atomic.Int64
	var failed atomic.Int64

	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range pairs {
				if err := expandOne(db, llmSvc, p.slug, p.lang); err != nil {
					failed.Add(1)
					fmt.Printf("[ERR] %s/%s — %v\n", p.slug, p.lang, err)
				} else {
					ok.Add(1)
					fmt.Printf("[OK ] %s/%s\n", p.slug, p.lang)
				}
			}
		}()
	}

	go func() {
		for _, j := range jobs {
			for _, l := range j.missing {
				pairs <- pair{slug: j.slug, lang: l}
			}
		}
		close(pairs)
	}()

	wg.Wait()

	fmt.Printf("\nDone. ok=%d failed=%d total=%d\n", ok.Load(), failed.Load(), totalNew)
	if failed.Load() > 0 {
		os.Exit(2)
	}
}

func expandOne(db *gorm.DB, llmSvc *llmService.UniversalLLMService, slug, targetLang string) error {
	// Idempotency: skip if already exists
	var existing int64
	if err := db.Table("w_prompt").Where("slug = ? AND lang = ?", slug, targetLang).Count(&existing).Error; err != nil {
		return fmt.Errorf("check existing: %w", err)
	}
	if existing > 0 {
		return nil
	}

	// Pull en source first, fallback zh
	var src model.Prompt
	if err := db.Where("slug = ? AND lang = 'en'", slug).First(&src).Error; err != nil {
		if err := db.Where("slug = ? AND lang = 'zh'", slug).First(&src).Error; err != nil {
			return fmt.Errorf("no en/zh source: %w", err)
		}
	}

	target := langDisplayName[targetLang]
	if target == "" {
		target = strings.ToUpper(targetLang)
	}

	// Translate prompt_content
	translatedContent, err := translate(llmSvc, src.PromptContent, target, fmt.Sprintf("expand_%s_%s_content", slug, targetLang))
	if err != nil {
		return fmt.Errorf("translate content: %w", err)
	}

	// Translate title (short, single LLM call)
	translatedTitle, err := translateTitle(llmSvc, src.Title, target, fmt.Sprintf("expand_%s_%s_title", slug, targetLang))
	if err != nil {
		return fmt.Errorf("translate title: %w", err)
	}

	// Build new row from src
	mainID := src.MainPromptID
	if mainID == 0 {
		mainID = src.Id
	}

	newRow := src
	newRow.GraMODEL.Id = 0
	newRow.GraMODEL.CreatedAt = time.Time{}
	newRow.GraMODEL.UpdatedAt = time.Time{}
	newRow.Lang = targetLang
	newRow.Title = translatedTitle
	newRow.PromptContent = translatedContent
	newRow.MainPromptID = mainID
	// Reset SEO so the template fills it later (or leave non-empty?)
	newRow.SeoTitle = fmt.Sprintf("%s - %s AI Prompt | WorkMax", translatedTitle, src.Category)
	newRow.SeoDescription = truncateRunes(translatedContent, 140)

	if err := db.Create(&newRow).Error; err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	return nil
}

func translate(llmSvc *llmService.UniversalLLMService, source, targetDisplay, requestID string) (string, error) {
	systemPrompt := `You are a professional translator specializing in AI image and video generation prompts.

Rules:
1. Translate to the requested target language only.
2. Preserve meaning, line breaks, paragraphs, list markers, punctuation, and spacing.
3. Keep model parameters, ratios, and English technical terms (Lora, ControlNet, "1:1", model names, brand names) unchanged.
4. Output only translated text. No JSON, no markdown fences, no explanations.`

	userMessage := fmt.Sprintf("Target language: %s\n\nTranslate while preserving formatting exactly.\n\n<source_prompt>\n%s\n</source_prompt>",
		targetDisplay, source)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resp, err := llmSvc.ProcessRequest(ctx, llmService.UniversalLLMRequest{
		SystemPrompt: systemPrompt,
		UserMessage:  userMessage,
		Temperature:  0.2,
		MaxTokens:    4000,
		Service:      "expand_translations",
		RequestID:    requestID,
	})
	if err != nil {
		return "", err
	}
	if resp == nil || resp.Content == "" {
		return "", fmt.Errorf("empty response")
	}
	return strings.TrimSpace(resp.Content), nil
}

// truncateRunes returns the first n runes of s. Avoids cutting in the
// middle of a multi-byte UTF-8 sequence which would produce invalid utf8mb4.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func translateTitle(llmSvc *llmService.UniversalLLMService, source, targetDisplay, requestID string) (string, error) {
	systemPrompt := "You translate short titles. Output only the translated title — no quotes, no explanations."
	userMessage := fmt.Sprintf("Target language: %s\nTranslate: %s", targetDisplay, source)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := llmSvc.ProcessRequest(ctx, llmService.UniversalLLMRequest{
		SystemPrompt: systemPrompt,
		UserMessage:  userMessage,
		Temperature:  0.2,
		MaxTokens:    200,
		Service:      "expand_translations_title",
		RequestID:    requestID,
	})
	if err != nil {
		return "", err
	}
	if resp == nil || resp.Content == "" {
		return source, nil
	}
	return strings.TrimSpace(strings.Trim(resp.Content, `"'`)), nil
}
