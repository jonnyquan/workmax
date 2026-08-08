package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"server/globals"
	"server/model"
	"server/utils"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// semRegistry holds per-provider concurrency-limit semaphores keyed by
// provider DB ID. Provider instances are now built per-call (no config
// cache), but the semaphore must be shared across calls or the limit
// becomes meaningless.
var semRegistry sync.Map // map[uint]*semEntry

type semEntry struct {
	limit int
	ch    chan struct{}
}

func acquireSemFor(providerID uint, limit int) chan struct{} {
	if providerID == 0 || limit <= 0 {
		return nil
	}
	if v, ok := semRegistry.Load(providerID); ok {
		entry := v.(*semEntry)
		if entry.limit == limit {
			return entry.ch
		}
		// Limit changed in DB. Replace; in-flight callers keep the old channel
		// reference and finish naturally — losing strict accounting for one
		// transition window is fine.
		semRegistry.Delete(providerID)
	}
	entry := &semEntry{limit: limit, ch: make(chan struct{}, limit)}
	v, _ := semRegistry.LoadOrStore(providerID, entry)
	return v.(*semEntry).ch
}

// fetchEnabledProviders queries the DB for all enabled providers, sorted by
// priority desc. Each lookup hits this fresh — no caching. Config changes
// (enable/disable, key rotation, priority shuffle) take effect on the next
// call.
func fetchEnabledProviders() ([]model.GeneratorProvider, error) {
	var rows []model.GeneratorProvider
	if err := globals.GraDBs["system"].
		Where("enabled = ?", true).
		Order("priority DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// rowMatchesProvider replaces the old matchesProvider that worked on built
// ImageProvider instances. Now we filter at the DB-row level so we don't
// instantiate providers we won't use.
func rowMatchesProvider(row *model.GeneratorProvider, providerType, mediaType string) bool {
	if row == nil || !row.Enabled {
		return false
	}
	if providerType != "" && !model.ProviderTypesEqual(row.Type, providerType) {
		return false
	}
	if mediaType == "" {
		return true
	}
	rowMedia := row.MediaType
	if rowMedia == "" {
		rowMedia = model.MediaTypeImage
	}
	if mediaType == model.MediaTypeImage {
		return rowMedia == model.MediaTypeImage
	}
	return rowMedia == mediaType
}

func rowConfiguredModelMatches(row *model.GeneratorProvider, modelID string) bool {
	if row == nil {
		return false
	}
	requested := model.NormalizeModelID(modelID)
	if requested == "" {
		return true
	}
	configured := strings.TrimSpace(row.Model)
	if configured == "" {
		return true
	}
	return model.NormalizeModelID(configured) == requested
}

func isVeoModel(modelID string) bool {
	switch model.NormalizeModelID(modelID) {
	case model.VEO_31, model.VEO_31_FAST:
		return true
	default:
		return false
	}
}

func rowMatchesProviderForModel(row *model.GeneratorProvider, mediaType, providerType, modelID string) bool {
	if rowMatchesProvider(row, providerType, mediaType) {
		return rowConfiguredModelMatches(row, modelID)
	}
	if row == nil || !row.Enabled {
		return false
	}
	if mediaType != "" {
		rowMedia := row.MediaType
		if rowMedia == "" {
			rowMedia = model.MediaTypeImage
		}
		if rowMedia != mediaType {
			return false
		}
	}
	if !rowConfiguredModelMatches(row, modelID) {
		return false
	}

	normalizedProviderType := model.NormalizeProviderType(row.Type)
	switch model.NormalizeModelID(modelID) {
	case model.NANO_BANANA, model.NANO_BANANA_2, model.NANO_BANANA_PRO:
		return normalizedProviderType == model.ProviderTypeGemini ||
			normalizedProviderType == model.ProviderTypeVertex
	case model.VEO_31, model.VEO_31_FAST:
		return normalizedProviderType == model.ProviderTypeGemini ||
			normalizedProviderType == model.ProviderTypeVertex
	default:
		return false
	}
}

// buildProvider constructs the wrapped ImageProvider for a DB row. Returns
// nil for unknown types or unbuildable configs.
func buildProvider(dbp *model.GeneratorProvider) ImageProvider {
	cfg := dbProviderToConfig(dbp)
	return createProvider(cfg)
}

// dbProviderToConfig 将数据库模型转换为配置
func dbProviderToConfig(dbp *model.GeneratorProvider) *ProviderConfig {
	var extraConfig *model.GeneratorProviderExtraConfig
	if dbp.ExtraConfig != "" {
		extraConfig = &model.GeneratorProviderExtraConfig{}
		if err := json.Unmarshal([]byte(dbp.ExtraConfig), extraConfig); err != nil {
			globals.Warn(fmt.Sprintf("Failed to parse ExtraConfig for provider %s: %v", dbp.Name, err))
			extraConfig = nil
		}
	}

	return &ProviderConfig{
		ProviderID:      dbp.Id,
		Name:            dbp.Name,
		Type:            dbp.Type,
		MediaType:       dbp.MediaType,
		Enabled:         dbp.Enabled,
		Endpoint:        dbp.Endpoint,
		APIKey:          dbp.APIKey,
		Model:           dbp.Model,
		DailyQuota:      dbp.DailyQuota,
		MonthlyQuota:    dbp.MonthlyQuota,
		ConcurrentLimit: dbp.ConcurrentLimit,
		ExtraConfig:     extraConfig,
	}
}

// createProvider 根据类型创建提供商
func createProvider(cfg *ProviderConfig) ImageProvider {
	var p ImageProvider
	normalizedType := model.NormalizeProviderType(cfg.Type)
	switch normalizedType {
	case model.ProviderTypeGemini:
		if cfg.MediaType == model.MediaTypeVideo || isVeoModel(cfg.Model) {
			p = NewVeoProvider(cfg)
		} else {
			p = NewNanoBananaProvider(cfg)
		}
	case model.ProviderTypeVertex:
		if cfg.MediaType == model.MediaTypeVideo || isVeoModel(cfg.Model) {
			p = NewVertexVeoProvider(cfg)
		} else {
			p = NewVertexGeminiImageProvider(cfg)
		}
	case model.ProviderTypeCustom:
		if cfg.MediaType == model.MediaTypeVideo {
			p = NewProxyVideoProvider(cfg)
		} else {
			p = NewProxyProvider(cfg)
		}
	case model.ProviderTypeKling:
		p = NewKlingProvider(cfg)
	case model.ProviderTypeSora:
		p = NewSoraProvider(cfg)
	case model.ProviderTypeMinimax:
		p = NewMiniMaxVideoProvider(cfg)
	case model.ProviderTypeSeedance:
		p = NewSeedanceProvider(cfg)
	case model.ProviderTypeReplicate:
		p = NewReplicateProvider(cfg)
	case model.ProviderTypeOpenAI:
		if model.NormalizeModelID(cfg.Model) == model.GPT_IMAGE_2 || strings.Contains(strings.ToLower(strings.TrimSpace(cfg.Model)), "gpt-image") {
			p = NewGPTImage2Provider(cfg)
		} else {
			p = NewProxyProvider(cfg)
		}
	case model.ProviderTypeStability:
		p = NewProxyProvider(cfg)
	default:
		globals.Error("Unknown provider type: " + cfg.Type)
		return nil
	}

	return wrapProvider(p, cfg)
}

type wrappedProvider struct {
	inner   ImageProvider
	timeout time.Duration
	retry   *utils.RetryConfig

	providerID uint
	name       string
	ptype      string
	mediaType  string
	model      string
	sem        chan struct{}
}

func wrapProvider(p ImageProvider, cfg *ProviderConfig) ImageProvider {
	if p == nil {
		return nil
	}

	retryCfg := utils.NewDefaultRetryConfig()
	retryCfg.RetryOnErrors = []string{
		"network_error",
		"timeout",
		"rate_limit_exceeded",
		"model_overloaded",
		"server_error_5xx",
	}

	timeout := time.Duration(0)
	if cfg != nil && cfg.ExtraConfig != nil {
		if cfg.ExtraConfig.Timeout > 0 {
			timeout = time.Duration(cfg.ExtraConfig.Timeout) * time.Second
		}
		if cfg.ExtraConfig.RetryCount > 0 {
			retryCfg.MaxRetries = cfg.ExtraConfig.RetryCount
		}
	}

	providerID := uint(0)
	providerName := ""
	providerType := ""
	providerMediaType := model.MediaTypeImage
	providerModel := ""
	concurrent := 0
	if cfg != nil {
		providerID = cfg.ProviderID
		providerName = cfg.Name
		providerType = model.NormalizeProviderType(cfg.Type)
		if cfg.MediaType != "" {
			providerMediaType = cfg.MediaType
		}
		providerModel = cfg.Model
		concurrent = cfg.ConcurrentLimit
	}

	return &wrappedProvider{
		inner:      p,
		timeout:    timeout,
		retry:      retryCfg,
		providerID: providerID,
		name:       providerName,
		ptype:      providerType,
		mediaType:  providerMediaType,
		model:      providerModel,
		sem:        acquireSemFor(providerID, concurrent),
	}
}

type ProviderSummary struct {
	Name      string
	Type      string
	MediaType string
	Model     string
}

func ListProviderSummariesByMediaType(mediaType string) []ProviderSummary {
	rows, err := fetchEnabledProviders()
	if err != nil {
		globals.Warn(fmt.Sprintf("ListProviderSummariesByMediaType: %v", err))
		return nil
	}
	summaries := make([]ProviderSummary, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		if !rowMatchesProvider(row, "", mediaType) {
			continue
		}
		mt := row.MediaType
		if mt == "" {
			mt = mediaType
			if mt == "" {
				mt = model.MediaTypeImage
			}
		}
		summaries = append(summaries, ProviderSummary{
			Name:      row.Name,
			Type:      model.NormalizeProviderType(row.Type),
			MediaType: mt,
			Model:     row.Model,
		})
	}
	return summaries
}

func (p *wrappedProvider) Generate(ctx context.Context, req *GenerationRequest) (*GenerationResult, error) {
	if p.inner == nil {
		return &GenerationResult{
			Success:  false,
			Error:    "Provider not initialized",
			Provider: "",
		}, nil
	}

	callCtx := ctx
	if p.timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, p.timeout)
		defer cancel()
	}

	releaseSem, ok, err := p.acquireSemaphore(callCtx)
	if err != nil {
		return &GenerationResult{
			Success:  false,
			Error:    err.Error(),
			Provider: p.inner.Name(),
		}, nil
	}
	if !ok {
		return &GenerationResult{
			Success:  false,
			Error:    "Provider is busy, please retry later",
			Provider: p.inner.Name(),
		}, nil
	}
	if releaseSem != nil {
		defer releaseSem()
	}

	ok, quotaReason, err := p.reserveQuota(callCtx)
	if err != nil {
		return &GenerationResult{
			Success:  false,
			Error:    err.Error(),
			Provider: p.inner.Name(),
		}, nil
	}
	if !ok {
		if quotaReason != "" {
			go notifyProviderQuotaIssue(context.Background(), p.providerID, quotaReason, "")
		}
		return &GenerationResult{
			Success:  false,
			Error:    quotaReason,
			Provider: p.inner.Name(),
		}, nil
	}

	// Cost-aware retry: WithRetry only ever sees an `error` for failures
	// it should treat as retryable. We split provider failures into two
	// buckets and surface only the first as a retryable error:
	//
	//   1. Pre-submit / no-upstream-cost: innerErr != nil OR
	//      (Success=false AND ProviderJobID empty). Examples: network
	//      hiccup before the create call landed, gateway returned 4xx/5xx
	//      with no job id, payload validation rejected the request. Safe
	//      to retry — the upstream never charged us.
	//
	//   2. Post-submit / cost committed: Success=false AND ProviderJobID
	//      set. The gateway already accepted a job, the meter is running,
	//      and a retry would create a SECOND paid job for what the user
	//      paid us once. Bubble the failure as a non-error result so
	//      WithRetry returns immediately on this attempt.
	//
	// This is what protects async predictions providers (gpt-image-2,
	// sora, seedance, kling, veo, …) from doubling provider cost on
	// errors like "model overloaded" that surface AFTER the job was
	// created.
	var committedFailure *GenerationResult
	result, err := utils.WithRetry[*GenerationResult](callCtx, p.retry, func() (*GenerationResult, error) {
		res, innerErr := p.inner.Generate(callCtx, req)
		if innerErr != nil {
			return nil, innerErr
		}
		if res == nil {
			return nil, fmt.Errorf("empty provider response")
		}
		if !res.Success {
			if strings.TrimSpace(res.ProviderJobID) != "" {
				// Job is already on the gateway's billing meter — don't
				// resubmit. Stash the failure so the outer code can
				// surface it as the result.
				committedFailure = res
				return res, nil
			}
			if res.Error == "" {
				return nil, fmt.Errorf("provider failed without message")
			}
			return nil, fmt.Errorf("%s", res.Error)
		}
		return res, nil
	})

	if committedFailure != nil {
		p.recordOutcome(callCtx, false, committedFailure.Error)
		if isUpstreamQuotaError(committedFailure.Error) {
			go notifyProviderQuotaIssue(context.Background(), p.providerID, QuotaReasonUpstreamExhausted, committedFailure.Error)
		}
		if committedFailure.Provider == "" {
			committedFailure.Provider = p.inner.Name()
		}
		return committedFailure, nil
	}

	if err != nil {
		errMsg := err.Error()
		p.recordOutcome(callCtx, false, errMsg)
		if isUpstreamQuotaError(errMsg) {
			go notifyProviderQuotaIssue(context.Background(), p.providerID, QuotaReasonUpstreamExhausted, errMsg)
		}
		return &GenerationResult{
			Success:  false,
			Error:    errMsg,
			Provider: p.inner.Name(),
		}, nil
	}

	if result != nil && result.Provider == "" {
		result.Provider = p.inner.Name()
	}
	p.recordOutcome(callCtx, true, "")
	return result, nil
}

func (p *wrappedProvider) acquireSemaphore(ctx context.Context) (func(), bool, error) {
	if p.sem == nil {
		return nil, true, nil
	}

	select {
	case p.sem <- struct{}{}:
		return func() { <-p.sem }, true, nil
	case <-time.After(2 * time.Second):
		return nil, false, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

func (p *wrappedProvider) reserveQuota(ctx context.Context) (bool, string, error) {
	if p.providerID == 0 {
		return true, "", nil
	}

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	quotaReason := ""
	err := globals.GraDBs["system"].WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var dbp model.GeneratorProvider
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&dbp, "id = ?", p.providerID).Error; err != nil {
			return err
		}

		needsDailyReset := dbp.ResetDate == nil ||
			dbp.ResetDate.Year() != today.Year() ||
			dbp.ResetDate.Month() != today.Month() ||
			dbp.ResetDate.Day() != today.Day()
		needsMonthlyReset := dbp.ResetDate == nil ||
			dbp.ResetDate.Year() != today.Year() ||
			dbp.ResetDate.Month() != today.Month()

		if needsDailyReset {
			dbp.DailyUsed = 0
		}
		if needsMonthlyReset {
			dbp.MonthlyUsed = 0
		}
		dbp.ResetDate = &today

		if dbp.DailyQuota > 0 && dbp.DailyUsed >= dbp.DailyQuota {
			quotaReason = "daily_quota_exceeded"
			return tx.Model(&model.GeneratorProvider{}).
				Where("id = ?", dbp.Id).
				Updates(map[string]interface{}{
					"daily_used":   dbp.DailyUsed,
					"monthly_used": dbp.MonthlyUsed,
					"reset_date":   dbp.ResetDate,
				}).Error
		}
		if dbp.MonthlyQuota > 0 && dbp.MonthlyUsed >= dbp.MonthlyQuota {
			quotaReason = "monthly_quota_exceeded"
			return tx.Model(&model.GeneratorProvider{}).
				Where("id = ?", dbp.Id).
				Updates(map[string]interface{}{
					"daily_used":   dbp.DailyUsed,
					"monthly_used": dbp.MonthlyUsed,
					"reset_date":   dbp.ResetDate,
				}).Error
		}

		dbp.DailyUsed++
		dbp.MonthlyUsed++
		dbp.TotalRequests++
		usedAt := now

		return tx.Model(&model.GeneratorProvider{}).
			Where("id = ?", dbp.Id).
			Updates(map[string]interface{}{
				"daily_used":     dbp.DailyUsed,
				"monthly_used":   dbp.MonthlyUsed,
				"reset_date":     dbp.ResetDate,
				"total_requests": dbp.TotalRequests,
				"last_used_at":   &usedAt,
			}).Error
	})
	if err != nil {
		return false, "", err
	}
	if quotaReason != "" {
		return false, quotaReason, nil
	}
	return true, "", nil
}

func (p *wrappedProvider) recordOutcome(ctx context.Context, success bool, errMsg string) {
	if p.providerID == 0 {
		return
	}

	now := time.Now()
	updates := map[string]interface{}{}
	if success {
		updates["success_requests"] = gorm.Expr("success_requests + ?", 1)
		updates["last_error"] = ""
		updates["last_error_at"] = nil
	} else {
		msg := errMsg
		if len(msg) > 480 {
			msg = msg[:480]
		}
		updates["failed_requests"] = gorm.Expr("failed_requests + ?", 1)
		updates["last_error"] = msg
		updates["last_error_at"] = &now
	}

	if len(updates) == 0 {
		return
	}

	if err := globals.GraDBs["system"].
		WithContext(ctx).
		Model(&model.GeneratorProvider{}).
		Where("id = ?", p.providerID).
		Updates(updates).Error; err != nil {
		globals.Error(fmt.Sprintf("Failed to update provider stats: %v", err))
	}
}

func (p *wrappedProvider) Name() string {
	return p.inner.Name()
}

func (p *wrappedProvider) Type() string {
	return p.inner.Type()
}

func (p *wrappedProvider) IsEnabled() bool {
	return p.inner.IsEnabled()
}

func (p *wrappedProvider) Cancel(ctx context.Context, jobID string) error {
	cancelable, ok := p.inner.(CancelableProvider)
	if !ok {
		return fmt.Errorf("provider does not support cancel")
	}
	return cancelable.Cancel(ctx, jobID)
}

// pickProviderRow walks rows in the priority order they came back from the
// DB and returns the first row that satisfies BOTH the caller's predicate
// (preferred name / is_default / any) AND the type+mediaType filter.
func pickProviderRow(rows []model.GeneratorProvider, preferredName, providerType, mediaType string) *model.GeneratorProvider {
	if preferredName != "" {
		for i := range rows {
			if rows[i].Name == preferredName && rowMatchesProvider(&rows[i], providerType, mediaType) {
				return &rows[i]
			}
		}
	}
	for i := range rows {
		if rows[i].IsDefault && rowMatchesProvider(&rows[i], providerType, mediaType) {
			return &rows[i]
		}
	}
	for i := range rows {
		if rowMatchesProvider(&rows[i], providerType, mediaType) {
			return &rows[i]
		}
	}
	return nil
}

func pickProviderRowForModel(rows []model.GeneratorProvider, preferredName, providerType, mediaType, modelID string) *model.GeneratorProvider {
	matches := func(row *model.GeneratorProvider) bool {
		return rowMatchesProviderForModel(row, mediaType, providerType, modelID)
	}
	if preferredName != "" {
		for i := range rows {
			if rows[i].Name == preferredName && matches(&rows[i]) {
				return &rows[i]
			}
		}
	}
	for i := range rows {
		if rows[i].IsDefault && matches(&rows[i]) {
			return &rows[i]
		}
	}
	for i := range rows {
		if matches(&rows[i]) {
			return &rows[i]
		}
	}
	return nil
}

// GetProvider 根据名称获取提供商
func GetProvider(name string) ImageProvider {
	rows, err := fetchEnabledProviders()
	if err != nil {
		globals.Warn(fmt.Sprintf("GetProvider: %v", err))
		return nil
	}
	for i := range rows {
		if rows[i].Name == name {
			return buildProvider(&rows[i])
		}
	}
	return nil
}

// GetDefaultProvider 获取默认可用的提供商
func GetDefaultProvider(preferredName string) ImageProvider {
	rows, err := fetchEnabledProviders()
	if err != nil {
		globals.Warn(fmt.Sprintf("GetDefaultProvider: %v", err))
		return nil
	}
	if row := pickProviderRow(rows, preferredName, "", ""); row != nil {
		return buildProvider(row)
	}
	return nil
}

// GetDefaultProviderByType 获取指定类型的默认可用提供商
func GetDefaultProviderByType(providerType string, preferredName string) ImageProvider {
	rows, err := fetchEnabledProviders()
	if err != nil {
		globals.Warn(fmt.Sprintf("GetDefaultProviderByType: %v", err))
		return nil
	}
	if row := pickProviderRow(rows, preferredName, providerType, ""); row != nil {
		return buildProvider(row)
	}
	return nil
}

// GetDefaultProviderByMediaType 获取指定媒体类型和可选提供商类型的默认可用提供商
func GetDefaultProviderByMediaType(mediaType, providerType, preferredName string) ImageProvider {
	rows, err := fetchEnabledProviders()
	if err != nil {
		globals.Warn(fmt.Sprintf("GetDefaultProviderByMediaType: %v", err))
		return nil
	}
	if row := pickProviderRow(rows, preferredName, providerType, mediaType); row != nil {
		return buildProvider(row)
	}
	return nil
}

// GetDefaultProviderForModel resolves a provider using both the business model
// ID and media type. It lets model-equivalent providers (for example Gemini API
// and Vertex Gemini Image for gemini-3.1-flash-image-preview) compete by the
// same preferredName/is_default/priority rules without weakening exact type
// matching for other call sites.
func GetDefaultProviderForModel(mediaType, providerType, modelID, preferredName string) ImageProvider {
	rows, err := fetchEnabledProviders()
	if err != nil {
		globals.Warn(fmt.Sprintf("GetDefaultProviderForModel: %v", err))
		return nil
	}
	if row := pickProviderRowForModel(rows, preferredName, providerType, mediaType, modelID); row != nil {
		return buildProvider(row)
	}
	return nil
}

// GetDefaultBackgroundRemoverProvider 获取默认背景移除提供商。
// 优先返回原生 BackgroundRemoverProvider；若不存在则包装为过渡实现。
func GetDefaultBackgroundRemoverProvider(providerType, preferredName string) BackgroundRemoverProvider {
	rows, err := fetchEnabledProviders()
	if err != nil {
		globals.Warn(fmt.Sprintf("GetDefaultBackgroundRemoverProvider: %v", err))
		return nil
	}

	tryRow := func(row *model.GeneratorProvider) BackgroundRemoverProvider {
		if !rowMatchesProvider(row, providerType, model.MediaTypeImage) {
			return nil
		}
		p := buildProvider(row)
		if p == nil {
			return nil
		}
		if rp, ok := p.(BackgroundRemoverProvider); ok && rp.IsEnabled() {
			return rp
		}
		return NewTransitionalBackgroundRemoverProvider(p)
	}

	if preferredName != "" {
		for i := range rows {
			if rows[i].Name == preferredName {
				if rp := tryRow(&rows[i]); rp != nil {
					return rp
				}
			}
		}
	}
	for i := range rows {
		if rows[i].IsDefault {
			if rp := tryRow(&rows[i]); rp != nil {
				return rp
			}
		}
	}
	for i := range rows {
		if rp := tryRow(&rows[i]); rp != nil {
			return rp
		}
	}
	return nil
}

// GetDefaultUpscalerProvider 获取默认放大提供商。
// 优先返回原生 UpscalerProvider；若不存在则包装为过渡实现。
func GetDefaultUpscalerProvider(providerType, preferredName string) UpscalerProvider {
	rows, err := fetchEnabledProviders()
	if err != nil {
		globals.Warn(fmt.Sprintf("GetDefaultUpscalerProvider: %v", err))
		return nil
	}

	tryRow := func(row *model.GeneratorProvider) UpscalerProvider {
		if !rowMatchesProvider(row, providerType, model.MediaTypeImage) {
			return nil
		}
		p := buildProvider(row)
		if p == nil {
			return nil
		}
		if up, ok := p.(UpscalerProvider); ok && up.IsEnabled() {
			return up
		}
		return NewTransitionalUpscalerProvider(p)
	}

	if preferredName != "" {
		for i := range rows {
			if rows[i].Name == preferredName {
				if up := tryRow(&rows[i]); up != nil {
					return up
				}
			}
		}
	}
	for i := range rows {
		if rows[i].IsDefault {
			if up := tryRow(&rows[i]); up != nil {
				return up
			}
		}
	}
	for i := range rows {
		if up := tryRow(&rows[i]); up != nil {
			return up
		}
	}
	return nil
}

// GetAllProviders 获取所有启用的提供商
func GetAllProviders() []ImageProvider {
	rows, err := fetchEnabledProviders()
	if err != nil {
		globals.Warn(fmt.Sprintf("GetAllProviders: %v", err))
		return nil
	}
	out := make([]ImageProvider, 0, len(rows))
	for i := range rows {
		if p := buildProvider(&rows[i]); p != nil {
			out = append(out, p)
		}
	}
	return out
}
