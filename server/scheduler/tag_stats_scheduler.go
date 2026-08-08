package scheduler

import (
	"encoding/json"
	"fmt"
	"server/globals"
	"server/model"
	"time"

	"gorm.io/gorm"
)

// TagStatsScheduler 标签统计定时任务
//
// 每天凌晨 3 点重算下列派生数据：
//
//	w_prompt_tag.usage_count / trend_score      —— 按 (lang, slug) 聚合 w_prompt.tags
//	w_prompt_tag.is_popular                     —— usage_count >= 100 自动标记
//	w_prompt_category.prompt_count              —— 按 (category, lang) 聚合 w_prompt
//	w_prompt.popular_score / is_trending /      —— 综合 view/copy/like/rating
//	w_prompt.is_featured                          每语言 top-100 → is_trending
//	                                              每语言 top-20  → is_featured
//	w_prompt.search_vector                      —— 模型自带的搜索向量重建
//
// 计数策略（按语言）：每条 prompt 行（多语言行各算一份）独立贡献到自身
// 语言的 (slug, lang) 计数。这样 chip 条在每种语言下显示的数字与该语言
// 真实可见的 prompt 数量一致 —— 翻译覆盖少的语言不会因为去重 master 被
// 错误放大。
type TagStatsScheduler struct {
	isRunning bool
	stopChan  chan struct{}
}

func NewTagStatsScheduler() *TagStatsScheduler {
	return &TagStatsScheduler{stopChan: make(chan struct{})}
}

func (s *TagStatsScheduler) Start() {
	if s.isRunning {
		return
	}
	s.isRunning = true
	go s.run()
}

func (s *TagStatsScheduler) Stop() {
	if !s.isRunning {
		return
	}
	s.isRunning = false
	s.stopChan <- struct{}{}
}

func (s *TagStatsScheduler) run() {
	defer func() {
		if r := recover(); r != nil {
			globals.Error(fmt.Sprintf("Tag stats scheduler panic recovered: %v", r))
			time.Sleep(5 * time.Second)
			if s.isRunning {
				go s.run()
			}
		}
	}()

	globals.Info("Tag stats scheduler started")

	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 3, 0, 0, 0, now.Location())
	timer := time.NewTimer(next.Sub(now))
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			s.RunDaily()
			next = next.Add(24 * time.Hour)
			timer.Reset(time.Until(next))
		case <-s.stopChan:
			globals.Info("Tag stats scheduler stopped")
			return
		}
	}
}

// RunDaily 是 scheduler 的每日 3am 入口，只跑 popularity ——
// view_count 是唯一会逐日漂移的输入，trending/featured 名单需要
// 每天重切。tag / category / search_vector 的输入只在内容变更时
// 才会动，挪到 import 完成回调和手动 cmd 上。
func (s *TagStatsScheduler) RunDaily() {
	globals.Info("Tag stats: daily pass starting")
	s.updatePromptPopularity()
	globals.Info("Tag stats: daily pass completed")
}

// RunRefresh 给"内容变更触发"用 —— 重算 tag / category 计数。
// 适合 import_video_prompts 收尾、admin 改单条 prompt 后调用。
// 不重建 search_vector（25k 行单 UPDATE，远端慢链路下要数小时；
// 真要重建走 RunSearchVectors）。
func (s *TagStatsScheduler) RunRefresh() {
	globals.Info("Tag stats: refresh pass starting")
	s.updateTagStats()
	s.updateCategoryStats()
	globals.Info("Tag stats: refresh pass completed")
}

// RunSearchVectors 单独重建 search_vector —— 真正做内容编辑后才需要。
// 拆出来是因为这一步在远端慢链路下要数小时，跟其它阶段绑死会拖慢
// 整个 pass。
func (s *TagStatsScheduler) RunSearchVectors() {
	globals.Info("Tag stats: search-vector rebuild starting")
	s.rebuildSearchVectors()
	globals.Info("Tag stats: search-vector rebuild completed")
}

// updateTagStats 按 (lang, slug) 聚合 w_prompt.tags 写回 w_prompt_tag。
// 阈值 100 自动 flip is_popular。
func (s *TagStatsScheduler) updateTagStats() {
	db := globals.GraDBs["system"]
	if db == nil {
		globals.Error("Database not available for tag stats update")
		return
	}

	globals.Info("Tag usage: aggregating w_prompt.tags...")

	type promptRow struct {
		ID   uint   `gorm:"column:id;primaryKey"`
		Tags string `gorm:"column:tags"`
		Lang string `gorm:"column:lang"`
	}

	tagCountsByLang := map[string]map[string]int{}
	parseFails := 0
	scanned := 0

	// FindInBatches over remote MySQL: a single Find on 25k+ rows can
	// hang indefinitely if the network drops mid-stream (DSN has no
	// readTimeout). 5k/batch keeps each round-trip bounded and lets
	// progress be visible.
	var batch []promptRow
	if err := db.Table("w_prompt").
		Select("id, tags, lang").
		Where("status = 1 AND tags IS NOT NULL AND tags <> '' AND tags <> '[]'").
		FindInBatches(&batch, 5000, func(tx *gorm.DB, _ int) error {
			for _, p := range batch {
				scanned++
				var tags []string
				if err := json.Unmarshal([]byte(p.Tags), &tags); err != nil {
					parseFails++
					continue
				}
				bucket, ok := tagCountsByLang[p.Lang]
				if !ok {
					bucket = map[string]int{}
					tagCountsByLang[p.Lang] = bucket
				}
				for _, tag := range tags {
					if tag == "" {
						continue
					}
					bucket[tag]++
				}
			}
			globals.Info(fmt.Sprintf("Tag usage: scanned %d prompts so far...", scanned))
			return nil
		}).Error; err != nil {
		globals.Error(fmt.Sprintf("Tag usage: scan failed: %v", err))
		return
	}

	// Pivot lang->slug->count to slug->lang->count so we can issue ONE
	// UPDATE per slug spanning all 18 lang rows (CASE WHEN lang...).
	// 36 slugs * 1 query/slug ≈ 50s on slow links, vs 648 individual
	// UPDATEs that took >1s each in practice. Slugs in JSON but not
	// in the curated w_prompt_tag taxonomy stay orphaned (logged at
	// the end so admins can choose to add them).
	slugToLangCount := map[string]map[string]int{}
	for lang, byTag := range tagCountsByLang {
		for slug, count := range byTag {
			bucket, ok := slugToLangCount[slug]
			if !ok {
				bucket = map[string]int{}
				slugToLangCount[slug] = bucket
			}
			bucket[lang] = count
		}
	}

	var taxonomySlugs []string
	if err := db.Table("w_prompt_tag").Distinct("slug").Pluck("slug", &taxonomySlugs).Error; err != nil {
		globals.Error(fmt.Sprintf("Tag usage: read taxonomy: %v", err))
		return
	}
	taxonomySet := map[string]struct{}{}
	for _, s := range taxonomySlugs {
		taxonomySet[s] = struct{}{}
	}

	totalRows := int64(0)
	for _, slug := range taxonomySlugs {
		langCount := slugToLangCount[slug]

		caseSQL := "CASE lang"
		caseArgs := make([]any, 0, len(langCount)*2)
		for lang, count := range langCount {
			caseSQL += " WHEN ? THEN ?"
			caseArgs = append(caseArgs, lang, count)
		}
		caseSQL += " ELSE 0 END"

		// args layout: usage CASE args, trend CASE args, then slug for WHERE.
		allArgs := make([]any, 0, len(caseArgs)*2+1)
		allArgs = append(allArgs, caseArgs...)
		allArgs = append(allArgs, caseArgs...)
		allArgs = append(allArgs, slug)

		res := db.Exec(
			"UPDATE w_prompt_tag SET usage_count = "+caseSQL+", trend_score = "+caseSQL+" WHERE slug = ?",
			allArgs...,
		)
		if res.Error != nil {
			globals.Error(fmt.Sprintf("Tag usage: bulk update %s: %v", slug, res.Error))
			continue
		}
		totalRows += res.RowsAffected
	}

	// Orphan tags (in prompts but not curated) — surface for admin.
	orphans := 0
	for slug := range slugToLangCount {
		if _, ok := taxonomySet[slug]; !ok {
			orphans++
		}
	}

	// Threshold flip — matches imgo. Manual overrides get re-evaluated
	// each run; flip the threshold or add a whitelist column to pin.
	db.Exec("UPDATE w_prompt_tag SET is_popular = TRUE  WHERE usage_count >= 100 AND is_popular = FALSE")
	db.Exec("UPDATE w_prompt_tag SET is_popular = FALSE WHERE usage_count <  100 AND is_popular = TRUE")

	globals.Info(fmt.Sprintf(
		"Tag usage: %d rows refreshed across %d slugs (langs=%d orphan_slugs=%d parse_failures=%d)",
		totalRows, len(taxonomySlugs), len(tagCountsByLang), orphans, parseFails,
	))
}

// updateCategoryStats 按 (category, lang) 重算 w_prompt_category.prompt_count。
// Per-code bulk CASE 跟 tag 一样的策略 —— 用 1 query/code 替代 N×M 单查询，
// 远端 MySQL 链路慢的时候差距 100×。
func (s *TagStatsScheduler) updateCategoryStats() {
	db := globals.GraDBs["system"]
	if db == nil {
		globals.Error("Database not available for category stats update")
		return
	}

	globals.Info("Category counts: aggregating w_prompt by (category, lang)...")

	type row struct {
		Category string `gorm:"column:category"`
		Lang     string `gorm:"column:lang"`
		Count    int    `gorm:"column:count"`
	}
	var rows []row
	if err := db.Table("w_prompt").
		Select("category, lang, COUNT(*) AS count").
		Where("status = 1 AND category IS NOT NULL AND category <> ''").
		Group("category, lang").
		Scan(&rows).Error; err != nil {
		globals.Error(fmt.Sprintf("Category counts: scan: %v", err))
		return
	}

	codeToLangCount := map[string]map[string]int{}
	for _, r := range rows {
		bucket, ok := codeToLangCount[r.Category]
		if !ok {
			bucket = map[string]int{}
			codeToLangCount[r.Category] = bucket
		}
		bucket[r.Lang] = r.Count
	}

	var codes []string
	if err := db.Table("w_prompt_category").Distinct("code").Pluck("code", &codes).Error; err != nil {
		globals.Error(fmt.Sprintf("Category counts: read taxonomy: %v", err))
		return
	}

	totalRows := int64(0)
	for _, code := range codes {
		langCount := codeToLangCount[code]

		caseSQL := "CASE lang"
		caseArgs := make([]any, 0, len(langCount)*2)
		for lang, count := range langCount {
			caseSQL += " WHEN ? THEN ?"
			caseArgs = append(caseArgs, lang, count)
		}
		caseSQL += " ELSE 0 END"

		args := make([]any, 0, len(caseArgs)+1)
		args = append(args, caseArgs...)
		args = append(args, code)

		res := db.Exec(
			"UPDATE w_prompt_category SET prompt_count = "+caseSQL+" WHERE code = ?",
			args...,
		)
		if res.Error != nil {
			globals.Error(fmt.Sprintf("Category counts: bulk update %s: %v", code, res.Error))
			continue
		}
		totalRows += res.RowsAffected
	}
	globals.Info(fmt.Sprintf(
		"Category counts: %d rows refreshed across %d codes (rows_in_aggregation=%d)",
		totalRows, len(codes), len(rows),
	))
}

// updatePromptPopularity 重算 w_prompt.popular_score 并按语言切 top-100/20
// 标记 is_trending / is_featured。
func (s *TagStatsScheduler) updatePromptPopularity() {
	db := globals.GraDBs["system"]
	if db == nil {
		globals.Error("Database not available for prompt popularity update")
		return
	}

	globals.Info("Popularity: rescoring w_prompt...")

	res := db.Exec(`
		UPDATE w_prompt
		SET popular_score = FLOOR(view_count * 0.1 + copy_count * 2 + like_count * 3 + rating * 10)
		WHERE status = 1
	`)
	globals.Info(fmt.Sprintf("Popularity: scored %d prompts", res.RowsAffected))

	db.Exec("UPDATE w_prompt SET is_trending = 0 WHERE is_trending = 1")
	db.Exec(`
		UPDATE w_prompt p
		INNER JOIN (
			SELECT id FROM (
				SELECT id, lang,
					ROW_NUMBER() OVER (PARTITION BY lang ORDER BY popular_score DESC) AS rn
				FROM w_prompt WHERE status = 1
			) ranked WHERE rn <= 100
		) top ON p.id = top.id
		SET p.is_trending = 1
	`)

	db.Exec("UPDATE w_prompt SET is_featured = 0 WHERE is_featured = 1")
	db.Exec(`
		UPDATE w_prompt p
		INNER JOIN (
			SELECT id FROM (
				SELECT id, lang,
					ROW_NUMBER() OVER (PARTITION BY lang ORDER BY popular_score DESC) AS rn
				FROM w_prompt WHERE status = 1
			) ranked WHERE rn <= 20
		) top ON p.id = top.id
		SET p.is_featured = 1
	`)

	globals.Info("Popularity: trending/featured flags re-cut per language")
}

// rebuildSearchVectors 全表重建 search_vector，吞下 model 包做实际拼装。
func (s *TagStatsScheduler) rebuildSearchVectors() {
	db := globals.GraDBs["system"]
	if db == nil {
		globals.Error("Database not available for search vector rebuild")
		return
	}

	globals.Info("Search vectors: rebuilding...")
	total, updated := model.RebuildAllSearchVectors(db)
	globals.Info(fmt.Sprintf("Search vectors: %d/%d prompts updated", updated, total))
}
