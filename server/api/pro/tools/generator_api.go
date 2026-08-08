package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"server/globals"
	"server/model"
	"server/service"
	accountsvc "server/service/account"
	globalassetService "server/service/globalasset"
	projectsvc "server/service/project"
	toolsService "server/service/tools"
	"server/utils"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type GeneratorApi struct{}

const (
	maxGeneratorGenerateRequestBodyBytes     = 1 << 20
	maxGeneratorPromptRunes                  = 5000
	maxGeneratorNegativePromptRunes          = 5000
	maxGeneratorParamsJSONBytes              = 64 << 10
	maxGeneratorReferenceImageItems          = 4
	maxGeneratorReferenceImageURLLength      = 2048
	maxGeneratorInlineReferenceImageURLBytes = 256 << 10
)

type ToolPricingCatalogExamplesResponse struct {
	BasicImageCredits int `json:"basicImageCredits"`
	Nano2ImageCredits int `json:"nano2ImageCredits"`
	ProImageCredits   int `json:"proImageCredits"`
	VideoCredits      int `json:"videoCredits"`
}

type ToolPricingCatalogResponse struct {
	Examples ToolPricingCatalogExamplesResponse `json:"examples"`
}

func (a *GeneratorApi) GetModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    toolsService.ListImageModelAvailability(),
	})
}

func (a *GeneratorApi) GetPricingCatalog(c *gin.Context) {
	catalog := toolsService.GetToolPricingCatalog()
	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": ToolPricingCatalogResponse{
			Examples: ToolPricingCatalogExamplesResponse{
				BasicImageCredits: catalog.Examples.BasicImageCredits,
				Nano2ImageCredits: catalog.Examples.Nano2ImageCredits,
				ProImageCredits:   catalog.Examples.ProImageCredits,
				VideoCredits:      catalog.Examples.VideoCredits,
			},
		},
	})
}

func supportsProcessingCancel(task *model.GenerationTask) bool {
	if task == nil {
		return false
	}
	return strings.TrimSpace(task.ToolID) == model.TOOL_VIDEO_GENERATOR
}

func stripSplitRetryMetadata(requestData model.JSONMap, preserveAggregation bool) {
	if requestData == nil {
		return
	}

	delete(requestData, "splitBatchId")
	delete(requestData, "splitBatchSize")
	delete(requestData, "splitBatchIndex")
	delete(requestData, "idempotencyKey")
	if !preserveAggregation {
		delete(requestData, "skipRecord")
		delete(requestData, "aggregateMode")
		delete(requestData, "parentRecordId")
	}

	if paramsRaw, ok := requestData["params"]; ok {
		switch params := paramsRaw.(type) {
		case map[string]interface{}:
			delete(params, "splitBatchId")
			delete(params, "splitBatchSize")
			delete(params, "splitBatchIndex")
			delete(params, "idempotencyKey")
			if !preserveAggregation {
				delete(params, "skipRecord")
				delete(params, "aggregateMode")
				delete(params, "parentRecordId")
			}
		case model.JSONMap:
			delete(params, "splitBatchId")
			delete(params, "splitBatchSize")
			delete(params, "splitBatchIndex")
			delete(params, "idempotencyKey")
			if !preserveAggregation {
				delete(params, "skipRecord")
				delete(params, "aggregateMode")
				delete(params, "parentRecordId")
			}
		}
	}
}

func respondGeneratorHTTPError(c *gin.Context, status int, message string, errorCode string, reload bool) {
	data := gin.H{}
	if reload {
		data["reload"] = true
	}
	if strings.TrimSpace(errorCode) != "" {
		data["errorCode"] = errorCode
	}

	body := gin.H{
		"code":    status,
		"message": message,
		"data":    data,
		"error": gin.H{
			"message":     message,
			"userMessage": message,
		},
	}
	if strings.TrimSpace(errorCode) != "" {
		body["errorCode"] = errorCode
	}

	c.JSON(status, body)
}

func respondGeneratorSubmitError(c *gin.Context, status int, message string, errorCode string, data gin.H) {
	payload := gin.H{
		"success": false,
	}
	for key, value := range data {
		payload[key] = value
	}
	if strings.TrimSpace(errorCode) != "" {
		payload["errorCode"] = errorCode
	}

	body := gin.H{
		"code":    status,
		"message": message,
		"data":    payload,
		"error": gin.H{
			"message":     message,
			"userMessage": message,
		},
	}
	if strings.TrimSpace(errorCode) != "" {
		body["errorCode"] = errorCode
	}

	c.JSON(status, body)
}

func generatorToolIDs() []string {
	return []string{model.TOOL_IMAGE_GENERATOR}
}

type generateGuard struct {
	mu                    sync.Mutex
	perMinute             int
	perUserConcurrent     int
	globalConcurrent      int
	users                 map[uint]*generateUserState
	cleanupAfterIdle      time.Duration
	cleanupInterval       time.Duration
	lastCleanup           time.Time
	globalConcurrentInUse int
}

type generateUserState struct {
	windowMinute int64
	requests     int
	concurrent   int
	lastSeen     time.Time
}

func newGenerateGuard(perMinute, perUserConcurrent, globalConcurrent int) *generateGuard {
	return &generateGuard{
		perMinute:         perMinute,
		perUserConcurrent: perUserConcurrent,
		globalConcurrent:  globalConcurrent,
		users:             map[uint]*generateUserState{},
		cleanupAfterIdle:  30 * time.Minute,
		cleanupInterval:   10 * time.Minute,
		lastCleanup:       time.Now(),
	}
}

func (g *generateGuard) acquire(uid uint) (func(), int, bool, string) {
	now := time.Now()

	g.mu.Lock()
	if now.Sub(g.lastCleanup) >= g.cleanupInterval {
		for k, v := range g.users {
			if now.Sub(v.lastSeen) >= g.cleanupAfterIdle && v.concurrent == 0 {
				delete(g.users, k)
			}
		}
		g.lastCleanup = now
	}

	state := g.users[uid]
	if state == nil {
		state = &generateUserState{
			windowMinute: now.Unix() / 60,
			lastSeen:     now,
		}
		g.users[uid] = state
	}

	windowMinute := now.Unix() / 60
	if state.windowMinute != windowMinute {
		state.windowMinute = windowMinute
		state.requests = 0
	}

	if g.perMinute > 0 && state.requests >= g.perMinute {
		next := time.Unix((windowMinute+1)*60, 0)
		retryAfter := int(time.Until(next).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		state.lastSeen = now
		g.mu.Unlock()
		return nil, retryAfter, false, "rate_limit"
	}

	if g.perUserConcurrent > 0 && state.concurrent >= g.perUserConcurrent {
		state.lastSeen = now
		g.mu.Unlock()
		return nil, 1, false, "concurrent_limit"
	}

	if g.globalConcurrent > 0 && g.globalConcurrentInUse >= g.globalConcurrent {
		state.lastSeen = now
		g.mu.Unlock()
		return nil, 1, false, "global_busy"
	}

	state.requests++
	state.concurrent++
	state.lastSeen = now
	g.globalConcurrentInUse++
	g.mu.Unlock()

	release := func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		cur := g.users[uid]
		if cur != nil && cur.concurrent > 0 {
			cur.concurrent--
		}
		if g.globalConcurrentInUse > 0 {
			g.globalConcurrentInUse--
		}
	}

	return release, 0, true, ""
}

// initGeneratorRequestGuard 初始化请求守卫
func initGeneratorRequestGuard() *generateGuard {
	cfg := globals.GraConf.Generator.RateLimit

	// 设置默认值（如果配置未设置）
	if cfg.PerMinute == 0 {
		cfg.PerMinute = 12
	}
	if cfg.PerUserConcurrent == 0 {
		cfg.PerUserConcurrent = 5
	}
	if cfg.GlobalConcurrent == 0 {
		cfg.GlobalConcurrent = 50
	}

	return newGenerateGuard(
		cfg.PerMinute,
		cfg.PerUserConcurrent,
		cfg.GlobalConcurrent,
	)
}

var (
	generatorRequestGuardOnce sync.Once
	generatorRequestGuard     *generateGuard
	submitIdempotencyMu       sync.Mutex
)

const submitIdempotencyReplayWindow = 24 * time.Hour

func getGeneratorRequestGuard() *generateGuard {
	generatorRequestGuardOnce.Do(func() {
		generatorRequestGuard = initGeneratorRequestGuard()
	})
	return generatorRequestGuard
}

// getActiveTaskLimit 获取最大活跃任务数
func getActiveTaskLimit() int {
	cfg := globals.GraConf.Generator.RateLimit
	if cfg.ActiveTaskLimit > 0 {
		return cfg.ActiveTaskLimit
	}
	return 5 // 默认值
}

// errTaskNotCancellable is a sentinel returned from inside the CancelTask
// transaction when the row-level guard finds the task already in a terminal
// state. The outer handler maps it to a 400 response.
var errTaskNotCancellable = errors.New("task not cancellable")

// releaseTaskReservationTx locks the GenerationTask owner before looking up
// its reservation for settlement. TTL only controls execution authorization;
// an expired-by-clock row still carries a real debit and must be released (or
// return its exact incompatible terminal outcome) rather than disappear behind
// FindActive.
func releaseTaskReservationTx(tx *gorm.DB, uid int, taskID string) error {
	if tx == nil {
		return fmt.Errorf("releaseTaskReservationTx: nil transaction")
	}
	var task model.GenerationTask
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "uid", "task_id").
		Where("uid = ? AND task_id = ?", uid, taskID).
		First(&task).Error; err != nil {
		return err
	}
	svc := accountsvc.NewCreditReservationService()
	reservation, err := svc.FindForSettlement(tx, uid, taskID)
	if err != nil {
		return err
	}
	if reservation == nil {
		return nil
	}
	if err := svc.Release(tx, reservation.Id); err != nil {
		return err
	}
	return nil
}

// releaseTaskReservation opens its own transaction and delegates to
// releaseTaskReservationTx. Use this from failure paths (enqueue fail, task
// fail) where no outer transaction is available.
func releaseTaskReservation(uid int, taskID string) error {
	return globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		return releaseTaskReservationTx(tx, uid, taskID)
	})
}

// releaseBatchReservations releases the per-task reservation for every task
// in the given slice. Used when the batch is aborted before any task gets a
// chance to run (queue unavailable / full).
func releaseBatchReservations(tasks []*model.GenerationTask) {
	for _, t := range tasks {
		if t == nil || t.TaskID == "" {
			continue
		}
		if err := releaseTaskReservation(t.UID, t.TaskID); err != nil {
			globals.Error(fmt.Sprintf("[Generator] Failed to release reservation for task %s: %v", t.TaskID, err))
		}
	}
}

// remainingCreditsForUID 现算用户当前可用 credits。PR3a 起替代 quota.GetCreditsRemaining()。
// 读取失败返回 0，避免让返回体字段为非法负值误导前端（原始错误由调用方日志记录）。
func remainingCreditsForUID(uid int) int {
	_, _, remaining, err := accountsvc.NewCreditsPackService().GetBalance(uid)
	if err != nil {
		globals.Error(fmt.Sprintf("[GeneratorApi] Failed to fetch credits balance for uid %d: %v", uid, err))
		return 0
	}
	return remaining
}

// GenerateRequest 生成请求
type GenerateRequest struct {
	Model           string                 `json:"model" binding:"required"`
	ModelCandidates []string               `json:"modelCandidates,omitempty"`
	Prompt          string                 `json:"prompt" binding:"required"`
	NegativePrompt  string                 `json:"negativePrompt"`
	AspectRatio     string                 `json:"aspectRatio"`
	StylePreset     string                 `json:"stylePreset"`
	Params          map[string]interface{} `json:"params"`
	ReferenceImages []ReferenceImageInput  `json:"referenceImages"`
	Resolution      string                 `json:"resolution,omitempty"`     // 分辨率: "2k" | "4k" (仅 Nano Banana Pro)
	NumberOfImages  int                    `json:"numberOfImages,omitempty"` // 图片数量: 1, 2, 3, 4 (仅 Nano Banana Pro)
	QuoteID         string                 `json:"quoteId,omitempty"`
	// ConsistencyVerdict — canvas video Proceed-Anyway audit payload
	// (#9 follow-up, 2026-05-15). Optional; only sent by the canvas
	// FE when the user clicked through a consistency warning. The
	// generator persists it verbatim onto request_data so the
	// canvas decision-log can read "did we warn this user about
	// this shot?" later. Shape mirrors
	// canvas/decision_log.go::DecisionLogVerdictRecord.
	ConsistencyVerdict map[string]interface{} `json:"consistencyVerdict,omitempty"`
}

// ReferenceImageInput 参考图片输入
type ReferenceImageInput struct {
	ID      string  `json:"id"`
	AssetID uint    `json:"assetId,omitempty"`
	URL     string  `json:"url"`
	Weight  float32 `json:"weight"`
}

func sanitizeGeneratorReferenceImages(items []ReferenceImageInput) ([]ReferenceImageInput, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > maxGeneratorReferenceImageItems {
		return nil, fmt.Errorf("too many reference images")
	}

	out := make([]ReferenceImageInput, 0, len(items))
	for _, item := range items {
		imageURL, err := sanitizeGeneratorReferenceImageURL(item.URL)
		if err != nil {
			return nil, err
		}
		if imageURL == "" {
			continue
		}
		id, weight, err := normalizeGeneratorReferenceImageMeta(item)
		if err != nil {
			return nil, err
		}
		out = append(out, ReferenceImageInput{
			ID:      id,
			AssetID: item.AssetID,
			URL:     imageURL,
			Weight:  weight,
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func resolveGeneratorReferenceImages(items []ReferenceImageInput, uid uint) ([]ReferenceImageInput, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > maxGeneratorReferenceImageItems {
		return nil, fmt.Errorf("too many reference images")
	}
	out := make([]ReferenceImageInput, 0, len(items))
	repo := globalassetService.NewRepository(globals.GraDBs["system"])
	for _, item := range items {
		id, weight, err := normalizeGeneratorReferenceImageMeta(item)
		if err != nil {
			return nil, err
		}
		if item.AssetID > 0 {
			if uid == 0 {
				return nil, fmt.Errorf("invalid reference image asset owner")
			}
			asset, err := repo.LoadForAccess(item.AssetID, int(uid))
			if err != nil {
				return nil, fmt.Errorf("reference image asset not found")
			}
			if asset.Kind != model.GlobalAssetKindImage {
				return nil, fmt.Errorf("reference image asset must be an image")
			}
			imageURL := strings.TrimSpace(asset.URL)
			if imageURL == "" {
				imageURL = strings.TrimSpace(asset.ThumbURL)
			}
			if imageURL == "" || len(imageURL) > maxGeneratorReferenceImageURLLength {
				return nil, fmt.Errorf("invalid reference image asset url")
			}
			if _, err := sanitizeGeneratorReferenceImageURL(imageURL); err != nil {
				return nil, err
			}
			if err := validateGeneratorReferenceImageURLForOwner(imageURL, uid); err != nil {
				return nil, err
			}
			out = append(out, ReferenceImageInput{
				ID:      id,
				AssetID: item.AssetID,
				URL:     imageURL,
				Weight:  weight,
			})
			continue
		}
		imageURL, err := sanitizeGeneratorReferenceImageURL(item.URL)
		if err != nil {
			return nil, err
		}
		if imageURL == "" {
			continue
		}
		if err := validateGeneratorReferenceImageURLForOwner(imageURL, uid); err != nil {
			return nil, err
		}
		out = append(out, ReferenceImageInput{
			ID:     id,
			URL:    imageURL,
			Weight: weight,
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func normalizeGeneratorReferenceImageMeta(item ReferenceImageInput) (string, float32, error) {
	id, err := sanitizeVideoReferenceField(item.ID, true)
	if err != nil {
		return "", 0, fmt.Errorf("invalid reference image id")
	}
	weight := item.Weight
	if math.IsNaN(float64(weight)) || math.IsInf(float64(weight), 0) || weight <= 0 {
		weight = 1
	}
	if weight < 0.1 {
		weight = 0.1
	}
	if weight > 2 {
		weight = 2
	}
	return id, weight, nil
}

func sanitizeGeneratorReferenceImageURL(raw string) (string, error) {
	if hasControlRune(raw) {
		return "", fmt.Errorf("invalid reference image url")
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if strings.HasPrefix(trimmed, "//") {
		return "", fmt.Errorf("invalid reference image url")
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "data:image/") {
		if len(trimmed) > maxGeneratorInlineReferenceImageURLBytes || !strings.Contains(lower, ";base64,") {
			return "", fmt.Errorf("invalid reference image data url")
		}
		return trimmed, nil
	}
	if len(trimmed) > maxGeneratorReferenceImageURLLength {
		return "", fmt.Errorf("reference image url is too long")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid reference image url")
	}
	if parsed.IsAbs() {
		scheme := strings.ToLower(parsed.Scheme)
		if scheme != "http" && scheme != "https" {
			return "", fmt.Errorf("invalid reference image url scheme")
		}
		if parsed.Host == "" || parsed.User != nil {
			return "", fmt.Errorf("invalid reference image url")
		}
		if !isKnownGeneratorReferencePath(parsed.Path) {
			return "", fmt.Errorf("reference image must come from uploaded assets")
		}
		if !isAllowedVideoReferenceMediaHost(parsed.Hostname()) {
			return "", fmt.Errorf("reference image host is not allowed")
		}
	} else if !strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("invalid reference image url")
	} else if !isKnownGeneratorReferencePath(parsed.Path) {
		return "", fmt.Errorf("reference image must come from uploaded assets")
	}

	pathValue := parsed.Path
	if pathValue == "" {
		pathValue = parsed.EscapedPath()
	}
	if hasUnsafeURLPath(pathValue) {
		return "", fmt.Errorf("invalid reference image url path")
	}
	return trimmed, nil
}

func isKnownGeneratorReferencePath(pathValue string) bool {
	return strings.HasPrefix(pathValue, "/uploads/")
}

func validateGeneratorReferenceImageURLForOwner(raw string, uid uint) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.HasPrefix(strings.ToLower(trimmed), "data:image/") {
		return nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("invalid reference image url")
	}
	pathValue := parsed.Path
	if pathValue == "" {
		pathValue = parsed.EscapedPath()
	}
	if !strings.HasPrefix(pathValue, "/uploads/") {
		return nil
	}
	if !referenceURLPathMatchesUID(pathValue, uid) {
		return fmt.Errorf("reference image does not belong to the current user")
	}
	return nil
}

// GenerateResponse 生成响应
type GenerateResponse struct {
	Success          bool     `json:"success"`
	ImageURLs        []string `json:"imageUrls,omitempty"`
	TaskID           string   `json:"taskId,omitempty"`
	Message          string   `json:"message,omitempty"`
	CreditsUsed      int      `json:"creditsUsed"`
	CreditsRemaining int      `json:"creditsRemaining"`
	Duration         int64    `json:"duration,omitempty"`
}

type generatorCreditQuoteResponse struct {
	Model          string `json:"model"`
	Resolution     string `json:"resolution,omitempty"`
	NumberOfImages int    `json:"numberOfImages"`
	Upscale        bool   `json:"upscale"`
	Quality        string `json:"quality,omitempty"`
	Credits        int    `json:"credits"`
}

// TaskSubmitResponse 任务提交响应
type TaskSubmitResponse struct {
	Success          bool     `json:"success"`
	TaskID           string   `json:"taskId"`
	TaskIDs          []string `json:"taskIds,omitempty"`
	BatchID          string   `json:"batchId,omitempty"`
	Status           string   `json:"status"`
	Message          string   `json:"message,omitempty"`
	ErrorCode        string   `json:"errorCode,omitempty"`
	Position         int      `json:"position,omitempty"` // 队列位置
	CreditsRemaining int      `json:"creditsRemaining"`
	UnitCreditCost   int      `json:"unitCreditCost,omitempty"`
	RequestedCount   int      `json:"requestedCount,omitempty"`
	ReservedCredits  int      `json:"reservedCredits,omitempty"`
	Idempotent       bool     `json:"idempotent,omitempty"`
	EffectiveSeed    int64    `json:"effectiveSeed,omitempty"`
}

func (a *GeneratorApi) GetCredits(c *gin.Context) {
	modelID := model.NormalizeModelID(c.DefaultQuery("model", model.NANO_BANANA))
	if modelID == "" {
		modelID = model.NANO_BANANA
	}

	toolID := toolsService.GetToolIDFromModelStr(modelID)
	if toolID != model.TOOL_IMAGE_GENERATOR {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid model", "INVALID_MODEL", false)
		return
	}

	numberOfImages, err := strconv.Atoi(strings.TrimSpace(c.DefaultQuery("numberOfImages", "1")))
	if err != nil {
		numberOfImages = 1
	}
	if numberOfImages < 1 {
		numberOfImages = 1
	}
	if numberOfImages > 4 {
		numberOfImages = 4
	}

	resolution := strings.TrimSpace(c.DefaultQuery("resolution", ""))
	upscale := false
	switch strings.ToLower(strings.TrimSpace(c.DefaultQuery("upscale", ""))) {
	case "1", "true", "yes":
		upscale = true
	}
	quality := strings.ToLower(strings.TrimSpace(c.DefaultQuery("quality", "")))

	switch modelID {
	case model.NANO_BANANA_PRO:
		if resolution == "" {
			resolution = "2k"
		}
		if resolution != "2k" && resolution != "4k" {
			respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid resolution", "INVALID_RESOLUTION", false)
			return
		}
		quality = ""
	case model.GPT_IMAGE_2:
		// GPT Image 2 cost scales with both resolution and quality (see
		// pricing_catalog.go). Default the resolution to 1k so missing
		// query params still produce a base estimate. upscale is N/A.
		if resolution == "" {
			resolution = "1k"
		}
		if resolution != "1k" && resolution != "2k" && resolution != "4k" && resolution != "auto" {
			respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid resolution", "INVALID_RESOLUTION", false)
			return
		}
		switch quality {
		case "", "auto", "low", "medium", "high":
			// ok
		default:
			respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid quality", "INVALID_QUALITY", false)
			return
		}
		upscale = false
	default:
		resolution = ""
		upscale = false
		quality = ""
	}

	credits := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID:         model.TOOL_IMAGE_GENERATOR,
		Model:          modelID,
		NumberOfImages: numberOfImages,
		Resolution:     resolution,
		Upscale:        upscale,
		Params: map[string]interface{}{
			"model":          modelID,
			"numberOfImages": numberOfImages,
			"resolution":     resolution,
			"upscale":        upscale,
			"quality":        quality,
		},
	})
	if credits <= 0 {
		credits = 1
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": generatorCreditQuoteResponse{
			Model:          modelID,
			Resolution:     resolution,
			NumberOfImages: numberOfImages,
			Upscale:        upscale,
			Quality:        quality,
			Credits:        credits,
		},
	})
}

// TaskStatusResponse 任务状态响应
type TaskStatusResponse struct {
	TaskID         string                 `json:"taskId"`
	RecordID       uint                   `json:"recordId,omitempty"`
	Status         string                 `json:"status"`
	Progress       int                    `json:"progress"`
	ProviderStatus string                 `json:"providerStatus,omitempty"`
	ProviderJobID  string                 `json:"providerJobId,omitempty"`
	ImageURLs      []string               `json:"imageUrls,omitempty"`
	VideoURLs      []string               `json:"videoUrls,omitempty"`
	OutputURLs     []string               `json:"outputUrls,omitempty"`
	ThumbnailURL   string                 `json:"thumbnailUrl,omitempty"`
	ResultMetadata map[string]interface{} `json:"resultMetadata,omitempty"`
	Error          string                 `json:"error,omitempty"`
	ErrorCode      string                 `json:"errorCode,omitempty"`
	CreatedAt      string                 `json:"createdAt"`
	StartedAt      *string                `json:"startedAt,omitempty"`
	CompletedAt    *string                `json:"completedAt,omitempty"`
	CreditsUsed    int                    `json:"creditsUsed"`
	DurationMs     int                    `json:"durationMs"`
}

type UploadReferenceImageResponse struct {
	Success       bool   `json:"success"`
	ID            string `json:"id,omitempty"`
	URL           string `json:"url,omitempty"`
	AssetID       uint   `json:"assetId,omitempty"`
	GlobalAssetID uint   `json:"globalAssetId,omitempty"`
	Message       string `json:"message,omitempty"`
}

// getActualCreditCostFromTask 从任务的 RequestData 中获取实际扣款金额
func getActualCreditCostFromTask(task *model.GenerationTask) int {
	if task == nil {
		return 0
	}
	if task.CreditsUsed > 0 {
		return task.CreditsUsed
	}

	modelType := task.Model
	if task.RequestData != nil {
		if m, ok := task.RequestData["model"].(string); ok && m != "" {
			modelType = m
		}
	}

	return toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID: task.ToolID,
		Model:  modelType,
		Params: task.RequestData,
	})
}

var (
	allowedAspectRatios = map[string]struct{}{
		"auto": {},
		"1:1":  {},
		"2:1":  {},
		"16:9": {},
		"9:16": {},
		"3:2":  {},
		"2:3":  {},
		"4:5":  {},
		"5:4":  {},
		"4:3":  {},
		"3:4":  {},
		"21:9": {},
		"9:21": {},
	}
	allowedStylePresets = map[string]struct{}{
		"anime":        {},
		"realistic":    {},
		"artistic":     {},
		"photo":        {},
		"cinematic":    {},
		"fantasy":      {},
		"cyberpunk":    {},
		"watercolor":   {},
		"oil-painting": {},
		"3d-render":    {},
	}
	allowedSamplers = map[string]struct{}{
		"euler":           {},
		"euler_a":         {},
		"dpm++_2m":        {},
		"dpm++_2m_karras": {},
		"dpm++_sde":       {},
		"ddim":            {},
		"uni_pc":          {},
	}
)

func normalizeIdempotencyKey(raw string) string {
	key := strings.TrimSpace(raw)
	if key == "" {
		return ""
	}
	if len(key) > 128 {
		key = key[:128]
	}
	return key
}

func buildSubmitErrorCode(reason string) string {
	switch reason {
	case "rate_limit":
		return "RATE_LIMITED"
	case "concurrent_limit":
		return "CONCURRENT_LIMIT"
	case "global_busy":
		return "QUEUE_BUSY"
	default:
		return "TASK_SUBMIT_FAILED"
	}
}

func normalizeModelCandidates(primary string, candidates []string) []string {
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(candidates)+1)
	appendIfValid := func(raw string) {
		modelID := model.NormalizeModelID(raw)
		if modelID == "" {
			return
		}
		if _, exists := seen[modelID]; exists {
			return
		}
		seen[modelID] = struct{}{}
		normalized = append(normalized, modelID)
	}

	appendIfValid(primary)
	for _, candidate := range candidates {
		appendIfValid(candidate)
	}
	return normalized
}

func filterModelCandidates(candidates []string, blocklist map[string]struct{}) []string {
	if len(candidates) == 0 || len(blocklist) == 0 {
		return candidates
	}
	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, blocked := blocklist[candidate]; blocked {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func readGeneratorStringParam(params map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if params == nil {
			return ""
		}
		raw, ok := params[key]
		if !ok {
			continue
		}
		if value, ok := raw.(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func readGeneratorIntParam(params map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		if params == nil {
			return 0
		}
		raw, ok := params[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case int:
			return value
		case int32:
			return int(value)
		case int64:
			return int(value)
		case float32:
			return int(value)
		case float64:
			return int(value)
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func normalizeGenerateVideoPayload(
	modelID string,
	aspectRatio string,
	resolution string,
	params map[string]interface{},
	referenceImages []ReferenceImageInput,
) (string, string, map[string]interface{}, string, error) {
	normalizedParams := map[string]interface{}{}
	for key, value := range params {
		normalizedParams[key] = value
	}

	normalizedAspectRatio := strings.TrimSpace(aspectRatio)
	if normalizedAspectRatio == "" {
		normalizedAspectRatio = "16:9"
	}
	if _, ok := allowedAspectRatios[normalizedAspectRatio]; !ok {
		return "", "", nil, "INVALID_ASPECT_RATIO", fmt.Errorf("invalid aspectRatio")
	}

	duration := normalizeVideoDuration(readGeneratorIntParam(normalizedParams, "duration"))
	generationMethod := readGeneratorStringParam(normalizedParams, "generationMethod")
	if generationMethod == "" {
		generationMethod = "frames"
	}
	motionMode := readGeneratorStringParam(normalizedParams, "motionMode")
	if motionMode == "" {
		motionMode = "std"
	}
	motionOrientation := readGeneratorStringParam(normalizedParams, "motionOrientation")
	if motionOrientation == "" {
		motionOrientation = "image"
	}
	startFrame := readGeneratorStringParam(normalizedParams, "startFrame", "startFrameUrl")
	endFrame := readGeneratorStringParam(normalizedParams, "endFrame", "endFrameUrl")
	if startFrame == "" || endFrame == "" {
		for _, ref := range referenceImages {
			refID := strings.TrimSpace(ref.ID)
			refURL := strings.TrimSpace(ref.URL)
			if refURL == "" {
				continue
			}
			switch refID {
			case "start-frame":
				if startFrame == "" {
					startFrame = refURL
				}
			case "end-frame":
				if endFrame == "" {
					endFrame = refURL
				}
			}
		}
	}

	hasStartFrame := startFrame != ""
	hasEndFrame := endFrame != ""
	if err := toolsService.ValidateVideoRequest(modelID, toolsService.VideoQuoteOptions{
		Duration:          duration,
		Resolution:        strings.TrimSpace(resolution),
		AspectRatio:       normalizedAspectRatio,
		MotionMode:        motionMode,
		MotionOrientation: motionOrientation,
		HasStartFrame:     hasStartFrame,
		HasEndFrame:       hasEndFrame,
	}, generationMethod, hasStartFrame, hasEndFrame); err != nil {
		code := "INVALID_VIDEO_REQUEST"
		switch err.Error() {
		case "invalid duration":
			code = "INVALID_VIDEO_DURATION"
		case "invalid resolution":
			code = "INVALID_VIDEO_RESOLUTION"
		case "invalid aspectRatio":
			code = "INVALID_ASPECT_RATIO"
		case "invalid generationMethod":
			code = "INVALID_GENERATION_METHOD"
		case "invalid motionOrientation":
			code = "INVALID_MOTION_ORIENTATION"
		}
		return "", "", nil, code, err
	}

	normalized := toolsService.NormalizeVideoQuoteOptions(modelID, toolsService.VideoQuoteOptions{
		Duration:          duration,
		Resolution:        strings.TrimSpace(resolution),
		AspectRatio:       normalizedAspectRatio,
		MotionMode:        motionMode,
		MotionOrientation: motionOrientation,
		HasStartFrame:     hasStartFrame,
		HasEndFrame:       hasEndFrame,
	})

	normalizedParams["mediaType"] = model.MediaTypeVideo
	normalizedParams["duration"] = strconv.Itoa(normalized.Duration)
	normalizedParams["generationMethod"] = generationMethod
	normalizedParams["motionMode"] = normalized.MotionMode
	if normalized.MotionOrientation != "" {
		normalizedParams["motionOrientation"] = normalized.MotionOrientation
	} else {
		delete(normalizedParams, "motionOrientation")
	}
	if startFrame != "" {
		normalizedParams["startFrame"] = startFrame
	}
	if endFrame != "" {
		normalizedParams["endFrame"] = endFrame
	}
	if normalized.Resolution != "" {
		normalizedParams["resolution"] = normalized.Resolution
	}

	return normalized.AspectRatio, normalized.Resolution, normalizedParams, "", nil
}

func modelCandidatesContain(target string, candidates []string) bool {
	target = model.NormalizeModelID(target)
	if target == "" || len(candidates) == 0 {
		return false
	}
	for _, candidate := range candidates {
		if model.NormalizeModelID(candidate) == target {
			return true
		}
	}
	return false
}

// submitTaskParams 提交任务的公共参数
type submitTaskParams struct {
	UID            uint
	ModelID        string
	ToolID         string
	CreditCost     int
	UnitCreditCost int
	RequestedCount int
	RequestData    model.JSONMap
	InputFiles     interface{}
	TaskItems      []submitTaskItem
	BatchID        string
	IdempotencyKey string
	EffectiveSeed  int64 // optional; echoed back in TaskSubmitResponse when non-zero
	// Origin stamps which product surface submitted — 'canvas',
	// 'video-gen', or 'image-gen'. Threaded into RequestData["origin"]
	// so it survives the queue and lands on w_generation_record.origin.
	// See canvas-tool-design.md §8.8.
	Origin string
	// ParentRecordID, when set, marks this submission as a continuation
	// of an existing GenerationRecord. Caller is responsible for
	// validating ownership; this struct just threads the value through
	// to the worker so saveGenerationRecord can persist it.
	ParentRecordID *uint

	// PostCreate runs inside the task-creation transaction once the
	// GenerationTask rows are persisted and reservations are booked.
	// Canvas uses this to write a CanvasTaskBinding row in the same tx
	// so the project↔task link is always atomic with the task itself.
	// Returning an error rolls the whole transaction back.
	PostCreate func(tx *gorm.DB, tasks []*model.GenerationTask) error
}

type submitTaskItem struct {
	ModelID     string
	ToolID      string
	CreditCost  int
	RequestData model.JSONMap
	InputFiles  interface{}
	// Origin overrides the batch-level origin for a single item. Leave
	// blank to inherit from submitTaskParams.Origin.
	Origin string
}

// resolveTaskItemOrigin picks the effective origin for a submit item,
// falling back from item-level → batch-level → an origin already stored
// in request_data (e.g. a retry that reuses the original task's data).
// Returns "" when none is available — legacy callers stay untouched.
func resolveTaskItemOrigin(itemOrigin, batchOrigin string, requestData model.JSONMap) string {
	if v := strings.TrimSpace(itemOrigin); v != "" {
		return v
	}
	if v := strings.TrimSpace(batchOrigin); v != "" {
		return v
	}
	if requestData != nil {
		if existing, ok := requestData["origin"].(string); ok {
			if v := strings.TrimSpace(existing); v != "" {
				return v
			}
		}
	}
	return ""
}

func cloneJSONMap(input model.JSONMap) model.JSONMap {
	out := model.JSONMap{}
	for k, v := range input {
		out[k] = v
	}
	return out
}

func getSubmitTaskRequiredSlots(items []submitTaskItem) int {
	if len(items) == 0 {
		return 1
	}

	splitBatchIDs := map[string]struct{}{}
	standaloneCount := 0

	for _, item := range items {
		if item.RequestData == nil {
			standaloneCount++
			continue
		}

		splitBatchID, _ := item.RequestData["splitBatchId"].(string)
		splitBatchID = strings.TrimSpace(splitBatchID)
		if splitBatchID == "" {
			standaloneCount++
			continue
		}

		splitBatchIDs[splitBatchID] = struct{}{}
	}

	requiredSlots := standaloneCount + len(splitBatchIDs)
	if requiredSlots <= 0 {
		return 1
	}
	return requiredSlots
}

// submitGenerationTask 提交生成任务的公共逻辑（检查配额/限流/事务/入队）
func (a *GeneratorApi) submitGenerationTask(c *gin.Context, p *submitTaskParams) {
	uid := p.UID
	items := p.TaskItems
	if len(items) == 0 {
		items = []submitTaskItem{{
			ModelID:     p.ModelID,
			ToolID:      p.ToolID,
			CreditCost:  p.CreditCost,
			RequestData: p.RequestData,
			InputFiles:  p.InputFiles,
			Origin:      p.Origin,
		}}
	}
	totalCreditCost := 0
	for _, item := range items {
		totalCreditCost += item.CreditCost
	}
	if totalCreditCost <= 0 {
		totalCreditCost = p.CreditCost
	}
	if totalCreditCost <= 0 {
		totalCreditCost = 1
	}
	unitCreditCost := p.UnitCreditCost
	if unitCreditCost <= 0 {
		unitCreditCost = totalCreditCost
		if len(items) > 0 && items[0].CreditCost > 0 {
			unitCreditCost = items[0].CreditCost
		}
	}
	requestedCount := p.RequestedCount
	if requestedCount <= 0 {
		requestedCount = len(items)
		if requestedCount <= 0 {
			requestedCount = 1
		}
	}
	primaryModelID := p.ModelID
	if primaryModelID == "" && len(items) > 0 {
		primaryModelID = items[0].ModelID
	}
	idempotencyKey := normalizeIdempotencyKey(p.IdempotencyKey)

	// 归一化任务项，并在 requestData 写入 idempotency key（用于后端查重）
	relatedToolIDSet := map[string]struct{}{}
	normalizedItems := make([]submitTaskItem, 0, len(items))
	for _, item := range items {
		requestData := cloneJSONMap(item.RequestData)
		if requestData == nil {
			requestData = model.JSONMap{}
		}
		if idempotencyKey != "" {
			requestData["idempotencyKey"] = idempotencyKey
		}
		// Thread origin into request_data so the TaskProcessor can lift it
		// into GenerateImageRequest.Origin and, eventually, onto
		// w_generation_record.origin — see canvas-tool-design.md §8.8.
		origin := resolveTaskItemOrigin(item.Origin, p.Origin, requestData)
		if origin != "" {
			requestData["origin"] = origin
		}
		// Thread lineage parent (the user-visible "continue from end
		// frame" chain) into request_data. Stored under a distinct key
		// from the canvas-aggregator's internal `parentRecordId` so the
		// strip-on-retry logic doesn't interfere — different semantics,
		// different scope, different lifecycle.
		if p.ParentRecordID != nil && *p.ParentRecordID > 0 {
			requestData["lineageParentRecordId"] = uint(*p.ParentRecordID)
		}
		toolID := strings.TrimSpace(item.ToolID)
		if toolID == "" {
			toolID = toolsService.GetToolIDFromModelStr(item.ModelID)
		}
		if toolID != "" {
			relatedToolIDSet[toolID] = struct{}{}
		}
		normalizedItems = append(normalizedItems, submitTaskItem{
			ModelID:     item.ModelID,
			ToolID:      toolID,
			CreditCost:  item.CreditCost,
			RequestData: requestData,
			InputFiles:  item.InputFiles,
			Origin:      origin,
		})
	}
	items = normalizedItems

	relatedToolIDs := make([]string, 0, len(relatedToolIDSet))
	for toolID := range relatedToolIDSet {
		relatedToolIDs = append(relatedToolIDs, toolID)
	}

	// 同 key 提交串行化，避免并发重复扣款。
	if idempotencyKey != "" {
		submitIdempotencyMu.Lock()
		defer submitIdempotencyMu.Unlock()
	}

	if idempotencyKey != "" {
		existingTasks, findErr := toolsService.GetRecentTasksByIdempotencyKey(int(uid), idempotencyKey, relatedToolIDs, submitIdempotencyReplayWindow)
		if findErr != nil {
			globals.Warn(fmt.Sprintf("[GeneratorApi] Failed to check idempotency key for user %d: %v", uid, findErr))
		}
		if len(existingTasks) > 0 {
			taskIDs := make([]string, 0, len(existingTasks))
			for _, task := range existingTasks {
				taskIDs = append(taskIDs, task.TaskID)
			}

			status := existingTasks[0].Status.String()
			batchID := ""
			if existingTasks[0].RequestData != nil {
				if v, ok := existingTasks[0].RequestData["splitBatchId"].(string); ok {
					batchID = strings.TrimSpace(v)
				}
			}

			creditsRemaining := remainingCreditsForUID(int(uid))

			c.JSON(http.StatusOK, gin.H{
				"code":    200,
				"message": "Task already submitted",
				"data": TaskSubmitResponse{
					Success:          true,
					TaskID:           taskIDs[0],
					TaskIDs:          taskIDs,
					BatchID:          batchID,
					Status:           status,
					Message:          "Idempotent replay",
					ErrorCode:        "IDEMPOTENT_REPLAY",
					CreditsRemaining: creditsRemaining,
					UnitCreditCost:   unitCreditCost,
					RequestedCount:   requestedCount,
					ReservedCredits:  totalCreditCost,
					Idempotent:       true,
				},
			})
			return
		}
	}

	// 预检 Credits 余额（现场 SUM）
	_, _, remainingCredits, err := accountsvc.NewCreditsPackService().GetBalance(int(uid))
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to get user credits", "USER_QUOTA_FETCH_FAILED", false)
		return
	}
	if remainingCredits < totalCreditCost {
		respondGeneratorSubmitError(c, http.StatusPaymentRequired, "Insufficient credits", "INSUFFICIENT_CREDITS", gin.H{
			"creditsRemaining": remainingCredits,
		})
		return
	}

	// 检查用户活跃任务数
	activeTaskLimit := getActiveTaskLimit()
	activeCount, _ := toolsService.GetActiveTaskCount(int(uid))
	requiredSlots := getSubmitTaskRequiredSlots(items)
	if int(activeCount)+requiredSlots > activeTaskLimit {
		respondGeneratorSubmitError(c, http.StatusTooManyRequests, "Too many active tasks. Please wait for current tasks to complete.", "ACTIVE_TASK_LIMIT", gin.H{
			"message":          fmt.Sprintf("You have %d active tasks. This request needs %d slots, maximum is %d.", activeCount, requiredSlots, activeTaskLimit),
			"creditsRemaining": remainingCredits,
		})
		return
	}

	// 限流检查
	release, retryAfter, ok, reason := getGeneratorRequestGuard().acquire(uid)
	if !ok {
		c.Header("Retry-After", strconv.Itoa(retryAfter))
		respondGeneratorSubmitError(c, http.StatusTooManyRequests, "Too many requests", buildSubmitErrorCode(reason), gin.H{
			"retryAfter": retryAfter,
			"reason":     reason,
		})
		return
	}
	defer release()

	// 使用事务处理扣款和任务创建
	var tasks []*model.GenerationTask
	err = globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		reservationSvc := accountsvc.NewCreditReservationService()

		tasks = make([]*model.GenerationTask, 0, len(items))
		for _, item := range items {
			toolID := strings.TrimSpace(item.ToolID)
			if toolID == "" {
				toolID = toolsService.GetToolIDFromModelStr(item.ModelID)
			}
			meta := &toolsService.UsageRecordMeta{
				IP:         utils.GetClientIP(c.Request),
				DeviceInfo: c.GetHeader("User-Agent"),
				ToolParams: item.RequestData,
				InputFiles: item.InputFiles,
			}
			task, createErr := toolsService.CreateTaskTx(tx, int(uid), toolID, item.ModelID, item.RequestData, item.CreditCost, meta)
			if createErr != nil {
				return fmt.Errorf("failed to create task: %w", createErr)
			}
			// Per-task reservation keyed by task.TaskID — a retry lands on the
			// same row; release/finalize later resolve it.
			//
			// TTL is sized to the task's runtime budget plus a 5-minute buffer
			// so the reservation outlives the task even on the slow end of the
			// timeout window. Without this, the expired-reservation sweeper
			// would refund credits mid-render for long-running video tasks.
			quoteID := ""
			if item.RequestData != nil {
				if v, ok := item.RequestData["quoteId"].(string); ok {
					quoteID = strings.TrimSpace(v)
				}
			}
			ttl := toolsService.GetTaskTimeoutForTask(task) + 5*time.Minute
			// P1 #6 slice 2 — when the caller is a canvas surface (X-Canvas-
			// Project-Id header set), gate the reservation against the
			// project's budget cap. Header is 0 for non-canvas callers and
			// the gate becomes a no-op.
			canvasProjectID := parseCanvasProjectHeader(c)
			if _, resErr := reservationSvc.Reserve(tx, accountsvc.ReservationRequest{
				UID:            int(uid),
				Tool:           "generator",
				IdempotencyKey: task.TaskID,
				QuoteID:        quoteID,
				Reserved:       item.CreditCost,
				TTL:            ttl,
				Remark:         fmt.Sprintf("generator %s", item.ModelID),
				ProjectID:      canvasProjectID,
			}); resErr != nil {
				return resErr
			}
			tasks = append(tasks, task)
		}

		if p.PostCreate != nil {
			if hookErr := p.PostCreate(tx, tasks); hookErr != nil {
				return fmt.Errorf("post-create hook failed: %w", hookErr)
			}
		}

		return nil
	})

	if err != nil {
		if errors.Is(err, accountsvc.ErrInsufficientCredits) {
			respondGeneratorSubmitError(c, http.StatusPaymentRequired, "Insufficient credits", "INSUFFICIENT_CREDITS", gin.H{
				"creditsRemaining": remainingCreditsForUID(int(uid)),
			})
		} else if errors.Is(err, projectsvc.ErrBudgetExceeded) {
			// P1 #6 slice 2 — canvas project budget cap reached. Surface
			// a distinct error code so the FE can render "increase cap
			// in settings, or run outside the project" rather than the
			// generic TASK_CREATE_FAILED.
			respondGeneratorSubmitError(c, http.StatusPaymentRequired, "Canvas project budget cap reached", "BUDGET_EXCEEDED", nil)
		} else {
			globals.Error(fmt.Sprintf("[GeneratorApi] Failed to create generation task for user %d: %v", uid, err))
			respondGeneratorSubmitError(c, http.StatusInternalServerError, "Failed to create generation task", "TASK_CREATE_FAILED", nil)
		}
		return
	}

	// 获取任务队列
	taskQueue := toolsService.GetGlobalTaskQueue()
	if taskQueue == nil {
		releaseBatchReservations(tasks)
		taskIDs := make([]uint, 0, len(tasks))
		for _, task := range tasks {
			taskIDs = append(taskIDs, task.Id)
		}
		_ = globals.GraDBs["system"].Model(&model.GenerationTask{}).Where("id IN ?", taskIDs).Updates(map[string]interface{}{
			"status":    model.TaskStatusFailed,
			"error_msg": "Task queue not available",
		}).Error

		respondGeneratorSubmitError(c, http.StatusServiceUnavailable, "Task queue not available. Please try again.", "QUEUE_UNAVAILABLE", nil)
		return
	}

	// 入队任务（批量原子检查容量）
	if err := taskQueue.EnqueueMany(tasks); err != nil {
		releaseBatchReservations(tasks)
		taskIDs := make([]uint, 0, len(tasks))
		for _, task := range tasks {
			taskIDs = append(taskIDs, task.Id)
		}
		_ = globals.GraDBs["system"].Model(&model.GenerationTask{}).Where("id IN ?", taskIDs).Updates(map[string]interface{}{
			"status":    model.TaskStatusFailed,
			"error_msg": err.Error(),
		}).Error

		respondGeneratorSubmitError(c, http.StatusServiceUnavailable, "Service is busy. Please try again later.", "QUEUE_BUSY", gin.H{
			"message":          "Task queue is full",
			"creditsRemaining": remainingCreditsForUID(int(uid)),
		})
		return
	}

	taskIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		taskIDs = append(taskIDs, task.TaskID)
	}
	primaryTaskID := ""
	if len(taskIDs) > 0 {
		primaryTaskID = taskIDs[0]
	}
	globals.Info(fmt.Sprintf("User %d submitted %d generation task(s) with model %s", uid, len(tasks), primaryModelID))

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "Task submitted successfully",
		"data": TaskSubmitResponse{
			Success:          true,
			TaskID:           primaryTaskID,
			TaskIDs:          taskIDs,
			BatchID:          p.BatchID,
			Status:           "pending",
			Position:         taskQueue.QueueLen(),
			CreditsRemaining: remainingCreditsForUID(int(uid)),
			UnitCreditCost:   unitCreditCost,
			RequestedCount:   requestedCount,
			ReservedCredits:  totalCreditCost,
			EffectiveSeed:    p.EffectiveSeed,
		},
	})
}

// Generate 提交生成任务（异步）
// @Summary 提交生成任务
// @Tags Tools
// @Accept json
// @Produce json
// @Param request body GenerateRequest true "生成请求"
// @Success 200 {object} TaskSubmitResponse
// @Router /api/tools/generate [post]
func (a *GeneratorApi) Generate(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxGeneratorGenerateRequestBodyBytes)

	var req GenerateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		globals.Warn(fmt.Sprintf("[GeneratorApi] Invalid generate request from user %d: %v", uid, err))
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid request", "INVALID_REQUEST", false)
		return
	}

	req.Prompt = strings.TrimSpace(req.Prompt)
	req.NegativePrompt = strings.TrimSpace(req.NegativePrompt)
	if req.Prompt == "" {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Prompt is required", "PROMPT_REQUIRED", false)
		return
	}
	if len([]rune(req.Prompt)) > maxGeneratorPromptRunes {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Prompt is too long", "PROMPT_TOO_LONG", false)
		return
	}
	if len([]rune(req.NegativePrompt)) > maxGeneratorNegativePromptRunes {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Negative prompt is too long", "NEGATIVE_PROMPT_TOO_LONG", false)
		return
	}
	if req.Params != nil {
		paramsJSON, err := json.Marshal(req.Params)
		if err != nil || len(paramsJSON) > maxGeneratorParamsJSONBytes {
			respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid params", "INVALID_PARAMS", false)
			return
		}
	}
	referenceImages, err := resolveGeneratorReferenceImages(req.ReferenceImages, uid)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusBadRequest, err.Error(), "INVALID_REFERENCE_IMAGE", false)
		return
	}
	req.ReferenceImages = referenceImages

	req.Model = model.NormalizeModelID(req.Model)
	req.ModelCandidates = normalizeModelCandidates(req.Model, req.ModelCandidates)

	modelCandidates := req.ModelCandidates
	needsProPermission := req.Model == model.NANO_BANANA_PRO || modelCandidatesContain(model.NANO_BANANA_PRO, modelCandidates)
	canUseProModel := true

	// 检查 Pro 模型权限（主模型或候选模型包含 Nano Banana Pro 时都要校验）
	if needsProPermission {
		permService := service.GroupServiceApp.AccountServiceGroup.PermissionService
		canUseProModel = permService.CanUseProModel(int(uid))
		if !canUseProModel && req.Model == model.NANO_BANANA_PRO {
			respondGeneratorHTTPError(c, http.StatusForbidden, "Pro model requires Pro subscription", "PRO_MODEL_REQUIRED", false)
			return
		}
	}
	if !canUseProModel {
		modelCandidates = filterModelCandidates(modelCandidates, map[string]struct{}{model.NANO_BANANA_PRO: {}})
		if len(modelCandidates) == 0 {
			modelCandidates = []string{req.Model}
		}
	}

	// 兼容旧客户端：从 params 里补齐 numberOfImages / resolution
	resolution := req.Resolution
	numberOfImages := req.NumberOfImages
	if req.Params != nil {
		if resolution == "" {
			if v, ok := req.Params["resolution"].(string); ok && v != "" {
				resolution = v
			}
		}
		if numberOfImages == 0 {
			if v, ok := req.Params["numberOfImages"].(float64); ok {
				numberOfImages = int(v)
			}
		}
	}
	if numberOfImages == 0 {
		numberOfImages = 1
	}
	if numberOfImages < 1 || numberOfImages > 4 {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid numberOfImages", "INVALID_NUMBER_OF_IMAGES", false)
		return
	}

	_, isVideoModel := toolsService.GetVideoModelCapabilities(req.Model)
	aspectRatio := strings.TrimSpace(req.AspectRatio)
	if isVideoModel {
		var errorCode string
		var normalizeErr error
		aspectRatio, resolution, req.Params, errorCode, normalizeErr = normalizeGenerateVideoPayload(req.Model, aspectRatio, resolution, req.Params, req.ReferenceImages)
		if normalizeErr != nil {
			respondGeneratorHTTPError(c, http.StatusBadRequest, normalizeErr.Error(), errorCode, false)
			return
		}
	} else if req.Model == model.NANO_BANANA_PRO {
		if resolution == "" {
			resolution = "2k"
		}
		if resolution != "2k" && resolution != "4k" {
			respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid resolution", "INVALID_RESOLUTION", false)
			return
		}
	} else if req.Model == model.GPT_IMAGE_2 {
		// GPT Image 2 supports resolution tiers (1k/2k/4k/auto) that
		// affect both billing AND the size parameter forwarded to the
		// provider. The previous "else" branch wiped resolution for
		// every non-pro non-video model, silently degrading 4k requests
		// to 1k at the provider while billing the cheaper tier.
		if resolution == "" {
			resolution = "1k"
		}
		if resolution != "1k" && resolution != "2k" && resolution != "4k" && resolution != "auto" {
			respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid resolution", "INVALID_RESOLUTION", false)
			return
		}
	} else {
		resolution = ""
	}

	if !isVideoModel {
		if aspectRatio == "" {
			aspectRatio = "1:1"
		}
		if _, ok := allowedAspectRatios[aspectRatio]; !ok {
			respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid aspectRatio", "INVALID_ASPECT_RATIO", false)
			return
		}
	}

	stylePreset := strings.TrimSpace(req.StylePreset)
	if stylePreset != "" {
		if _, ok := allowedStylePresets[stylePreset]; !ok {
			respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid stylePreset", "INVALID_STYLE_PRESET", false)
			return
		}
	}

	steps := 0
	cfgScale := 0.0
	sampler := ""
	seed := int64(-1)
	upscale := false
	if req.Params != nil {
		if v, ok := req.Params["steps"].(float64); ok {
			if math.Mod(v, 1) != 0 || v < 1 || v > 150 {
				respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid steps", "INVALID_STEPS", false)
				return
			}
			steps = int(v)
		}
		if v, ok := req.Params["cfgScale"].(float64); ok {
			if v <= 0 || v > 30 {
				respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid cfgScale", "INVALID_CFG_SCALE", false)
				return
			}
			cfgScale = v
		}
		if v, ok := req.Params["sampler"].(string); ok {
			if v != "" {
				if _, ok := allowedSamplers[v]; !ok {
					respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid sampler", "INVALID_SAMPLER", false)
					return
				}
				sampler = v
			}
		}
		if v, ok := req.Params["seed"].(float64); ok {
			if math.Mod(v, 1) != 0 {
				respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid seed", "INVALID_SEED", false)
				return
			}
			s := int64(v)
			if s < -1 || s > 2147483647 {
				respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid seed", "INVALID_SEED", false)
				return
			}
			seed = s
		}
		if v, ok := req.Params["upscale"].(bool); ok {
			upscale = v
		}
	}

	toolID := toolsService.GetToolIDFromModelStr(req.Model)
	// 计算需要消耗的 Credits
	creditCost := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID:         toolID,
		Model:          req.Model,
		NumberOfImages: numberOfImages,
		Resolution:     resolution,
		Upscale:        upscale,
		Params:         req.Params,
	})

	// Quote reconciliation: if the client sent a quoteId, verify the fingerprint
	// against the current request. Matched quotes become authoritative for the
	// singleton path so mid-session pricing drift can't cheat accounting; batch
	// reservations still pay per-item via singleCreditCost. Mismatches fall
	// through to the freshly computed cost and warn for monitoring.
	if trimmedQuoteID := strings.TrimSpace(req.QuoteID); trimmedQuoteID != "" {
		var (
			canonical map[string]interface{}
			quoteMode string
		)
		if isVideoModel {
			canonical = toolsService.CanonicalVideoQuoteParams(toolsService.NormalizeVideoQuoteOptions(req.Model, toolsService.VideoQuoteOptions{
				Duration:          intFromQuoteParams(req.Params, "duration"),
				Resolution:        resolution,
				AspectRatio:       aspectRatio,
				MotionMode:        stringFromQuoteParams(req.Params, "motionMode"),
				MotionOrientation: stringFromQuoteParams(req.Params, "motionOrientation"),
				HasStartFrame:     quoteParamPresent(req.Params, "startFrameUrl") || quoteParamPresent(req.Params, "startFrame"),
				HasEndFrame:       quoteParamPresent(req.Params, "endFrameUrl") || quoteParamPresent(req.Params, "endFrame"),
			}))
			quoteMode = "video"
		} else {
			canonical = toolsService.CanonicalImageQuoteParams(numberOfImages, resolution, stringFromQuoteParams(req.Params, "quality"))
			quoteMode = "image"
		}
		if rec, ok := toolsService.VerifyQuote(trimmedQuoteID, uid, quoteMode, req.Model, canonical); ok {
			creditCost = rec.Credits
			toolsService.ConsumeQuote(trimmedQuoteID)
		} else {
			globals.Warn(fmt.Sprintf("[GeneratorApi] quoteId %s rejected for user %d model %s mode %s — using fresh price %d", trimmedQuoteID, uid, req.Model, quoteMode, creditCost))
		}
	}

	// 构建请求数据
	requestData := model.JSONMap{
		"model":           req.Model,
		"prompt":          req.Prompt,
		"negativePrompt":  req.NegativePrompt,
		"aspectRatio":     aspectRatio,
		"stylePreset":     stylePreset,
		"steps":           steps,
		"cfgScale":        cfgScale,
		"sampler":         sampler,
		"seed":            seed,
		"upscale":         upscale,
		"referenceImages": req.ReferenceImages,
		"resolution":      resolution,
		"numberOfImages":  numberOfImages,
	}
	if len(req.Params) > 0 {
		requestData["params"] = req.Params
	}
	if len(modelCandidates) > 1 {
		requestData["modelCandidates"] = modelCandidates
	}
	// #9 follow-up: persist the FE-supplied consistency verdict so
	// the canvas decision-log can answer "did we warn this user
	// before they spent the credits?" later. Only stamped when the
	// payload is non-empty — non-canvas callers omit the field and
	// the row stays clean.
	if len(req.ConsistencyVerdict) > 0 {
		requestData["consistencyVerdict"] = req.ConsistencyVerdict
	}

	var inputFiles interface{}
	if len(req.ReferenceImages) > 0 {
		inputFiles = map[string]interface{}{
			"referenceImages": req.ReferenceImages,
		}
	}

	origin := model.GENERATION_ORIGIN_IMAGE_GEN
	var postCreate func(tx *gorm.DB, tasks []*model.GenerationTask) error
	if bindingCtx, status, message, errorCode := resolveCanvasTaskBindingContext(c, uid); status != 0 {
		respondGeneratorHTTPError(c, status, message, errorCode, false)
		return
	} else if bindingCtx != nil {
		origin = model.GENERATION_ORIGIN_CANVAS
		postCreate = bindingCtx.PostCreate
		requestData["source"] = "canvas"
		requestData["canvasOperation"] = "generate"
		if binding := parseCanvasAssetBindingsHeader(c); binding != nil {
			requestData["assetBindings"] = binding
		}
	}

	if numberOfImages > 1 {
		idempotencyKey := normalizeIdempotencyKey(c.GetHeader("X-Idempotency-Key"))
		if idempotencyKey == "" {
			idempotencyKey = normalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
		}
		singleCreditCost := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
			ToolID:         toolID,
			Model:          req.Model,
			NumberOfImages: 1,
			Resolution:     resolution,
			Upscale:        upscale,
			Params:         req.Params,
		})
		if singleCreditCost <= 0 {
			singleCreditCost = 1
		}
		batchID := fmt.Sprintf("batch_%s", uuid.New().String())
		items := make([]submitTaskItem, 0, numberOfImages)

		for i := 0; i < numberOfImages; i++ {
			taskRequestData := cloneJSONMap(requestData)
			taskRequestData["numberOfImages"] = 1
			taskRequestData["skipRecord"] = true
			taskRequestData["splitBatchId"] = batchID
			taskRequestData["splitBatchSize"] = numberOfImages
			taskRequestData["splitBatchIndex"] = i + 1

			items = append(items, submitTaskItem{
				ModelID:     req.Model,
				CreditCost:  singleCreditCost,
				RequestData: taskRequestData,
				InputFiles:  inputFiles,
			})
		}

		a.submitGenerationTask(c, &submitTaskParams{
			UID:            uid,
			ModelID:        req.Model,
			CreditCost:     creditCost,
			TaskItems:      items,
			BatchID:        batchID,
			IdempotencyKey: idempotencyKey,
			PostCreate:     postCreate,
			Origin:         origin,
		})
		return
	}

	idempotencyKey := normalizeIdempotencyKey(c.GetHeader("X-Idempotency-Key"))
	if idempotencyKey == "" {
		idempotencyKey = normalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	}

	a.submitGenerationTask(c, &submitTaskParams{
		UID:            uid,
		ModelID:        req.Model,
		CreditCost:     creditCost,
		RequestData:    requestData,
		InputFiles:     inputFiles,
		IdempotencyKey: idempotencyKey,
		PostCreate:     postCreate,
		Origin:         origin,
	})
}

// Wait-loop tuning. Default deadline of 25s keeps responses returning
// before the typical reverse-proxy / load-balancer 30s idle timeout.
//
// The previous 500ms DB tick is gone — task_queue mutation sites now
// publish to TaskEventBus and we wait on the channel instead. A defensive
// 5s "backstop" tick still forces a DB re-read in case some mutation
// path slipped publish instrumentation; in practice the bus carries
// every change, so the backstop fires only on a misuse.
const (
	taskWaitBackstopInterval  = 5 * time.Second
	taskWaitDefaultTimeoutSec = 25
	taskWaitMaxTimeoutSec     = 30
)

func isTaskWaitRequested(c *gin.Context) bool {
	v := strings.ToLower(strings.TrimSpace(c.Query("wait")))
	return v == "true" || v == "1"
}

func isTaskTerminalStatus(s model.TaskStatus) bool {
	return s == model.TaskStatusCompleted || s == model.TaskStatusFailed || s == model.TaskStatusCancelled
}

// waitForTaskChange holds the connection until the task's status or
// progress diverges from what the client says it last saw, the task
// reaches a terminal state, the deadline elapses, or the client
// disconnects. Returns the freshest task pointer; on error (task
// vanished mid-wait), returns the original task so the caller can
// decide how to surface that. Set hasError=true only when the original
// taskID could not be re-fetched.
//
// Implementation: subscribes to TaskEventBus before the first guard
// check (subscribe-then-check guards against the publish landing
// during subscribe). On wake, drains any pending bus signal and
// re-reads the task. A 5s backstop ticker provides a safety net if
// some mutation site slipped publish instrumentation — in normal
// operation the bus delivers every change, so the backstop is dead
// weight that only matters under regressions.
func waitForTaskChange(c *gin.Context, task *model.GenerationTask) (*model.GenerationTask, bool) {
	sinceStatus := strings.TrimSpace(c.Query("since_status"))
	sinceProgress := -1
	if raw := strings.TrimSpace(c.Query("since_progress")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			sinceProgress = v
		}
	}
	timeoutSec := taskWaitDefaultTimeoutSec
	if raw := strings.TrimSpace(c.Query("timeout")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			timeoutSec = v
		}
	}
	if timeoutSec > taskWaitMaxTimeoutSec {
		timeoutSec = taskWaitMaxTimeoutSec
	}

	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	// Subscribe BEFORE the first guard check so any publish that fires
	// between our subscribe-time and the check is captured in the
	// channel buffer. Guard then drains; either the change is already
	// reflected in `current` (in which case we return) or we wait on
	// the channel.
	eventCh, unsubscribe := toolsService.SubscribeTaskEvent(task.TaskID)
	defer unsubscribe()

	backstop := time.NewTicker(taskWaitBackstopInterval)
	defer backstop.Stop()

	current := task
	for {
		terminal := isTaskTerminalStatus(current.Status)
		statusChanged := sinceStatus != "" && !strings.EqualFold(sinceStatus, current.Status.String())
		progressChanged := sinceProgress >= 0 && current.Progress != sinceProgress
		if terminal || statusChanged || progressChanged {
			return current, false
		}
		if time.Now().After(deadline) {
			return current, false
		}

		// Wait on the bus, the backstop tick, or the deadline / client
		// disconnect. A timer scoped to the remaining deadline keeps
		// the goroutine bounded even when no events fire.
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return current, false
		}
		deadlineTimer := time.NewTimer(remaining)
		select {
		case <-c.Request.Context().Done():
			// Client disconnected — bail without writing a response;
			// caller checks ctx.Err() before any further write.
			deadlineTimer.Stop()
			return current, true
		case <-eventCh:
			deadlineTimer.Stop()
		case <-backstop.C:
			deadlineTimer.Stop()
		case <-deadlineTimer.C:
			return current, false
		}

		next, err := toolsService.GetTaskByID(current.TaskID)
		if err != nil {
			// Task vanished while we were waiting (extremely rare —
			// only happens on hard delete). Surface as not-found.
			return current, true
		}
		current = next
	}
}

// GetTaskStatus 获取任务状态
// @Summary 获取任务状态
// @Tags Tools
// @Accept json
// @Produce json
// @Param taskId path string true "任务ID"
// @Param wait query bool false "Long-poll: hold the connection until status / progress changes"
// @Param since_status query string false "Wait until task status differs from this value"
// @Param since_progress query int false "Wait until task progress differs from this value"
// @Param timeout query int false "Long-poll deadline in seconds (default 25, max 30)"
// @Success 200 {object} TaskStatusResponse
// @Router /api/tools/task/{taskId} [get]
func (a *GeneratorApi) GetTaskStatus(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	taskID := c.Param("taskId")
	if taskID == "" {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Task ID is required", "TASK_ID_REQUIRED", false)
		return
	}

	task, err := toolsService.GetTaskByID(taskID)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusNotFound, "Task not found", "TASK_NOT_FOUND", false)
		return
	}

	// 验证任务所有权
	if task.UID != int(uid) {
		respondGeneratorHTTPError(c, http.StatusForbidden, "Access denied", "TASK_ACCESS_DENIED", false)
		return
	}

	// Long-poll: when the client passes ?wait=true, hold the connection
	// until task status / progress changes from what they say they last
	// saw. Reduces /task/:id call rate from one-per-N-seconds to one-
	// per-state-change. Server-side cost is one goroutine per blocked
	// caller plus a 500ms DB poll — fine at our scale; pub/sub is the
	// next step if waiter counts grow.
	if isTaskWaitRequested(c) {
		updated, errored := waitForTaskChange(c, task)
		if c.Request.Context().Err() != nil {
			// Client gave up; don't bother writing a response.
			return
		}
		if errored {
			respondGeneratorHTTPError(c, http.StatusNotFound, "Task not found", "TASK_NOT_FOUND", false)
			return
		}
		task = updated
	}

	response := TaskStatusResponse{
		TaskID:      task.TaskID,
		RecordID:    task.RecordID,
		Status:      task.Status.String(),
		Progress:    task.Progress,
		Error:       task.ErrorMsg,
		ErrorCode:   toolsService.ClassifyTaskErrorCode(task.ErrorMsg),
		CreatedAt:   task.CreatedAt.Format(time.RFC3339),
		CreditsUsed: task.CreditsUsed,
		DurationMs:  task.DurationMs,
	}

	if task.StartedAt != nil {
		formatted := task.StartedAt.Format(time.RFC3339)
		response.StartedAt = &formatted
	}

	if task.CompletedAt != nil {
		formatted := task.CompletedAt.Format(time.RFC3339)
		response.CompletedAt = &formatted
	}

	if task.Status == model.TaskStatusCompleted && task.ResultData != nil {
		(&toolsService.GenerationObjectService{}).ResolveTaskDownloadURLs(c.Request.Context(), task)
		response.ImageURLs = toolsService.ParseImageURLs(task.ResultData)
		response.VideoURLs = toolsService.ParseVideoURLs(task.ResultData)
		response.OutputURLs = toolsService.ParseOutputURLs(task.ResultData)
		response.ThumbnailURL = toolsService.ParseThumbnailURL(task.ResultData)
		if metadata, ok := task.ResultData["resultMetadata"].(map[string]interface{}); ok {
			response.ResultMetadata = metadata
		} else if metadata, ok := task.ResultData["resultMetadata"].(model.JSONMap); ok {
			response.ResultMetadata = map[string]interface{}(metadata)
		}
	}

	if task.ResultData != nil {
		if raw, ok := task.ResultData["providerStatus"].(string); ok {
			response.ProviderStatus = strings.TrimSpace(raw)
		}
		if response.ProviderStatus == "" {
			if raw, ok := task.ResultData["status"].(string); ok {
				response.ProviderStatus = strings.TrimSpace(raw)
			}
		}
		if raw, ok := task.ResultData["providerJobId"].(string); ok {
			response.ProviderJobID = strings.TrimSpace(raw)
		}
	}

	if task.Status == model.TaskStatusProcessing && task.StartedAt != nil && response.Progress <= 0 {
		taskTimeout := toolsService.GetTaskTimeoutForTask(task)
		if taskTimeout <= 0 {
			taskTimeout = 5 * time.Minute
		}
		elapsed := time.Since(*task.StartedAt)
		estimate := int(math.Round(float64(elapsed) / float64(taskTimeout) * 90))
		if estimate < response.Progress {
			estimate = response.Progress
		}
		if estimate > 90 {
			estimate = 90
		}
		response.Progress = estimate
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    response,
	})
}

// GetActiveTasks 获取用户的活跃任务
// @Summary 获取活跃任务
// @Tags Tools
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/tools/tasks/active [get]
func (a *GeneratorApi) GetActiveTasks(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	tasks, err := toolsService.GetActiveTasksByUIDAndTool(int(uid), generatorToolIDs())
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to get active tasks", "ACTIVE_TASKS_FETCH_FAILED", false)
		return
	}

	result := make([]map[string]interface{}, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, toolsService.TaskToResponseDTOWithContext(c.Request.Context(), &task))
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    result,
	})
}

func (a *GeneratorApi) GetTasks(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	tasks, err := toolsService.GetRecentTasksByUIDAndTool(int(uid), generatorToolIDs(), limit)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to get tasks", "TASKS_FETCH_FAILED", false)
		return
	}

	result := make([]map[string]interface{}, 0, len(tasks))
	for _, task := range tasks {
		dto := toolsService.TaskToResponseDTOWithContext(c.Request.Context(), &task)
		if task.RequestData != nil {
			dto["requestData"] = task.RequestData
		}
		result = append(result, dto)
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    result,
	})
}

func (a *GeneratorApi) RetryTask(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	taskID := c.Param("taskId")
	if taskID == "" {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Task ID is required", "TASK_ID_REQUIRED", false)
		return
	}

	task, err := toolsService.GetTaskByID(taskID)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusNotFound, "Task not found", "TASK_NOT_FOUND", false)
		return
	}
	if task.UID != int(uid) {
		respondGeneratorHTTPError(c, http.StatusForbidden, "Access denied", "TASK_ACCESS_DENIED", false)
		return
	}
	if task.Status != model.TaskStatusFailed {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Only failed tasks can be retried", "TASK_NOT_RETRYABLE", false)
		return
	}
	if task.RequestData == nil {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Task has no request data", "TASK_REQUEST_DATA_MISSING", false)
		return
	}

	modelID, _ := task.RequestData["model"].(string)
	if strings.TrimSpace(modelID) == "" {
		modelID = task.Model
	}
	modelID = strings.TrimSpace(modelID)

	toolID := strings.TrimSpace(task.ToolID)
	if toolID == "" {
		toolID = toolsService.GetToolIDFromModelStr(modelID)
	}

	if model.NormalizeModelID(modelID) == model.NANO_BANANA_PRO {
		permService := service.GroupServiceApp.AccountServiceGroup.PermissionService
		if !permService.CanUseProModel(int(uid)) {
			respondGeneratorHTTPError(c, http.StatusForbidden, "Pro model requires Pro subscription", "PRO_MODEL_REQUIRED", false)
			return
		}
	}

	numberOfImages := 1
	if v, ok := task.RequestData["numberOfImages"]; ok {
		switch vv := v.(type) {
		case float64:
			numberOfImages = int(vv)
		case int:
			numberOfImages = vv
		}
	}
	if numberOfImages < 1 || numberOfImages > 4 {
		numberOfImages = 1
	}
	if toolID == model.TOOL_IMAGE_VECTORIZER {
		numberOfImages = 1
	}

	resolution := ""
	if v, ok := task.RequestData["resolution"].(string); ok {
		resolution = v
	}
	if model.NormalizeModelID(modelID) != model.NANO_BANANA_PRO {
		resolution = ""
	}

	creditCost := toolsService.GetCreditCostByToolID(toolsService.CreditCostParams{
		ToolID:         toolID,
		Model:          modelID,
		NumberOfImages: numberOfImages,
		Resolution:     resolution,
		Params:         task.RequestData,
	})

	newRequestData := model.JSONMap{}
	for k, v := range task.RequestData {
		newRequestData[k] = v
	}
	stripSplitRetryMetadata(newRequestData, false)
	if toolID == model.TOOL_IMAGE_VECTORIZER {
		newRequestData["numberOfImages"] = 1
		if paramsRaw, ok := newRequestData["params"]; ok {
			switch params := paramsRaw.(type) {
			case map[string]interface{}:
				params["numberOfImages"] = 1
			case model.JSONMap:
				params["numberOfImages"] = 1
			}
		}
	}

	var retryInputFiles interface{}
	if refs, ok := newRequestData["referenceImages"]; ok {
		retryInputFiles = map[string]interface{}{
			"referenceImages": refs,
		}
	}
	idempotencyKey := normalizeIdempotencyKey(c.GetHeader("X-Idempotency-Key"))
	if idempotencyKey == "" {
		idempotencyKey = normalizeIdempotencyKey(c.GetHeader("Idempotency-Key"))
	}

	a.submitGenerationTask(c, &submitTaskParams{
		UID:            uid,
		ModelID:        modelID,
		ToolID:         toolID,
		CreditCost:     creditCost,
		RequestData:    newRequestData,
		InputFiles:     retryInputFiles,
		IdempotencyKey: idempotencyKey,
	})
}

// CancelTask 取消任务
// @Summary 取消任务
// @Tags Tools
// @Accept json
// @Produce json
// @Param taskId path string true "任务ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/tools/task/{taskId}/cancel [post]
func (a *GeneratorApi) CancelTask(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	taskID := c.Param("taskId")
	if taskID == "" {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Task ID is required", "TASK_ID_REQUIRED", false)
		return
	}

	task, err := toolsService.GetTaskByID(taskID)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusNotFound, "Task not found", "TASK_NOT_FOUND", false)
		return
	}

	// 验证所有权
	if task.UID != int(uid) {
		respondGeneratorHTTPError(c, http.StatusForbidden, "Access denied", "TASK_ACCESS_DENIED", false)
		return
	}

	if task.Status != model.TaskStatusPending && task.Status != model.TaskStatusProcessing {
		respondGeneratorHTTPError(
			c,
			http.StatusBadRequest,
			fmt.Sprintf("Task cannot be cancelled, current status: %s", task.Status.String()),
			"TASK_NOT_CANCELLABLE",
			false,
		)
		return
	}

	// Once a task transitions to Processing, the worker has already dispatched
	// the request upstream. For providers we can't cleanly abort mid-flight
	// (image generators today), we refuse the cancel so the user doesn't lose
	// credits on a request we can't actually stop. The natural terminal
	// transition (success/failure/timeout) still refunds as appropriate.
	if task.Status == model.TaskStatusProcessing {
		if !supportsProcessingCancel(task) {
			respondGeneratorHTTPError(
				c,
				http.StatusConflict,
				"Task is already generating and cannot be cancelled",
				"TASK_ALREADY_PROCESSING",
				false,
			)
			return
		}
		if err := toolsService.CancelRemoteGenerationTask(c.Request.Context(), task); err != nil {
			if errors.Is(err, toolsService.ErrCancelUnsupported) {
				respondGeneratorHTTPError(c, http.StatusConflict, "Current provider does not support cancelling an in-progress task", "TASK_CANCEL_UNSUPPORTED", false)
				return
			}
			globals.Warn(fmt.Sprintf("[CancelTask] Remote cancel failed for task %s: %v", task.TaskID, err))
			respondGeneratorHTTPError(c, http.StatusBadGateway, "Failed to cancel upstream generation", "TASK_CANCEL_UPSTREAM_FAILED", false)
			return
		}
	}

	// Atomic: status transition + reservation release + refund sync. If any
	// step fails we roll back, so the task stays cancellable and no credits
	// are ever left reserved against a cancelled task (or vice versa).
	creditCost := getActualCreditCostFromTask(task)
	txErr := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		res := tx.Model(&model.GenerationTask{}).
			Where("task_id = ? AND uid = ? AND status IN ?", taskID, int(uid), []model.TaskStatus{model.TaskStatusPending, model.TaskStatusProcessing}).
			Updates(map[string]interface{}{
				"status":       model.TaskStatusCancelled,
				"error_msg":    "Cancelled by user",
				"completed_at": now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return errTaskNotCancellable
		}

		if creditCost <= 0 {
			return nil
		}
		if err := releaseTaskReservationTx(tx, task.UID, task.TaskID); err != nil {
			return err
		}
		resultData := task.ResultData
		if resultData == nil {
			resultData = model.JSONMap{}
		}
		resultData["refunded"] = true
		resultData["refundCredits"] = creditCost
		return tx.Model(&model.GenerationTask{}).
			Where("id = ?", task.Id).
			Updates(map[string]interface{}{
				"credits_used": 0,
				"result_data":  resultData,
			}).Error
	})
	if errors.Is(txErr, errTaskNotCancellable) {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Task cannot be cancelled", "TASK_NOT_CANCELLABLE", false)
		return
	}
	if txErr != nil {
		globals.Error(fmt.Sprintf("[CancelTask] atomic cancel+refund failed for %s: %v", taskID, txErr))
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to cancel task", "TASK_CANCEL_FAILED", false)
		return
	}

	if task.Status == model.TaskStatusProcessing {
		if queue := toolsService.GetGlobalTaskQueue(); queue != nil {
			queue.CancelActiveTask(taskID)
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "Task cancelled successfully",
		"data": gin.H{
			"success": true,
			"taskId":  taskID,
			"status":  "cancelled",
		},
	})
}

// GetHistory 获取生成历史
// @Summary 获取生成历史
// @Tags Tools
// @Accept json
// @Produce json
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Param model query string false "模型筛选"
// @Success 200 {object} map[string]interface{}
// @Router /api/tools/history [get]
func (a *GeneratorApi) GetHistory(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	modelFilter := c.Query("model")
	withTotal := c.DefaultQuery("withTotal", "1") != "0"

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}

	genService := service.GroupServiceApp.ToolServiceGroup.GeneratorService
	records, total, err := genService.GetGenerationHistoryByToolIDAndModel(uid, page, limit, generatorToolIDs(), modelFilter, withTotal)
	if err != nil {
		globals.Error(fmt.Sprintf("[GeneratorApi] Failed to get generation history for user %d: %v", uid, err))
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to get history", "HISTORY_FETCH_FAILED", false)
		return
	}

	objectService := &toolsService.GenerationObjectService{}
	for index := range records {
		objectService.ResolveRecordDownloadURLs(c.Request.Context(), &records[index])
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": gin.H{
			"items": records,
			"total": total,
			"page":  page,
			"limit": limit,
		},
	})
}

func (a *GeneratorApi) DeleteHistoryRecord(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	recordID, err := strconv.Atoi(strings.TrimSpace(c.Param("id")))
	if err != nil || recordID <= 0 {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Record ID is required", "RECORD_ID_REQUIRED", false)
		return
	}

	var record model.GenerationRecord
	if err := globals.GraDBs["system"].
		Where("id = ? AND uid = ? AND tool_id IN ?", recordID, uid, generatorToolIDs()).
		First(&record).Error; err != nil {
		respondGeneratorHTTPError(c, http.StatusNotFound, "Record not found", "RECORD_NOT_FOUND", false)
		return
	}

	if err := globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		return (&toolsService.GenerationObjectService{}).DeleteGenerationRecordsWithAssets(c.Request.Context(), tx, []model.GenerationRecord{record})
	}); err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to delete record", "RECORD_DELETE_FAILED", false)
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": 200, "message": "success", "data": gin.H{"id": recordID}})
}

// GetGenerationByID 根据 ID 获取生成记录
// @Summary 根据 ID 获取生成记录
// @Tags Tools
// @Accept json
// @Produce json
// @Param id path int true "记录ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/tools/generate/{id} [get]
func (a *GeneratorApi) GetGenerationByID(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid ID", "INVALID_RECORD_ID", false)
		return
	}

	genService := service.GroupServiceApp.ToolServiceGroup.GeneratorService
	record, err := genService.GetGenerationByID(uid, uint(id))
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusNotFound, "Record not found", "RECORD_NOT_FOUND", false)
		return
	}

	(&toolsService.GenerationObjectService{}).ResolveRecordDownloadURLs(c.Request.Context(), record)

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data":    record,
	})
}

func (a *GeneratorApi) UploadReferenceImage(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	maxSize := getMaxReferenceImageSize()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSize+referenceMediaMultipartOverheadBytes)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		fileHeader, err = c.FormFile("image")
		if err != nil {
			respondGeneratorHTTPError(c, http.StatusBadRequest, "Missing file", "UPLOAD_FILE_MISSING", false)
			return
		}
	}

	if fileHeader.Size > maxSize {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "File too large", "UPLOAD_FILE_TOO_LARGE", false)
		return
	}

	f, err := fileHeader.Open()
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to open file", "UPLOAD_OPEN_FAILED", false)
		return
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to read file", "UPLOAD_READ_FAILED", false)
		return
	}
	if int64(len(data)) > maxSize {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "File too large", "UPLOAD_FILE_TOO_LARGE", false)
		return
	}

	allowed := map[string]string{
		"image/png":  "png",
		"image/jpeg": "jpg",
		"image/webp": "webp",
		"image/gif":  "gif",
	}
	contentType, ok := resolveUploadedContentType(data, fileHeader.Header.Get("Content-Type"), allowed)
	if !ok {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid file type", "UPLOAD_INVALID_FILE_TYPE", false)
		return
	}
	ext, ok := allowed[contentType]
	if !ok {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid file type", "UPLOAD_INVALID_FILE_TYPE", false)
		return
	}

	genService := service.GroupServiceApp.ToolServiceGroup.GeneratorService
	result, err := genService.UploadReferenceImage(c.Request.Context(), uid, contentType, data, ext)
	if err != nil {
		globals.Error(fmt.Sprintf("[GeneratorApi] Reference image upload failed for user %d: %v", uid, err))
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Upload failed", "UPLOAD_FAILED", false)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": UploadReferenceImageResponse{
			Success:       true,
			ID:            result.ID,
			URL:           result.URL,
			AssetID:       result.GlobalAssetID,
			GlobalAssetID: result.GlobalAssetID,
		},
	})
}

// getMaxReferenceImageSize 代理到 service 层的统一实现
func getMaxReferenceImageSize() int64 {
	return toolsService.GetMaxReferenceImageSize()
}

var allowedReferenceVideoTypes = map[string]string{
	"video/mp4":        "mp4",
	"video/quicktime":  "mov",
	"video/webm":       "webm",
	"video/x-matroska": "mkv",
}

var allowedReferenceAudioTypes = map[string]string{
	"audio/mpeg":   "mp3",
	"audio/mp3":    "mp3",
	"audio/wav":    "wav",
	"audio/x-wav":  "wav",
	"audio/m4a":    "m4a",
	"audio/mp4":    "m4a",
	"audio/aac":    "aac",
	"audio/x-m4a":  "m4a",
	"audio/webm":   "weba",
	"audio/flac":   "flac",
	"audio/x-flac": "flac",
}

const referenceMediaMultipartOverheadBytes int64 = 1 << 20

func normalizeUploadContentType(raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if idx := strings.Index(normalized, ";"); idx >= 0 {
		normalized = strings.TrimSpace(normalized[:idx])
	}
	return normalized
}

func resolveUploadedContentType(data []byte, declared string, allowedTypes map[string]string) (string, bool) {
	declared = normalizeUploadContentType(declared)
	sniffed := normalizeUploadContentType(http.DetectContentType(data))
	if _, ok := allowedTypes[sniffed]; ok {
		if declared == "" || declared == sniffed {
			return sniffed, true
		}
		return "", false
	}
	if declared != "" {
		if _, ok := allowedTypes[declared]; ok && uploadMagicMatchesContentType(data, declared) {
			return declared, true
		}
	}
	return "", false
}

func uploadMagicMatchesContentType(data []byte, contentType string) bool {
	if len(data) == 0 {
		return false
	}
	switch contentType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return normalizeUploadContentType(http.DetectContentType(data)) == contentType
	case "video/mp4", "video/quicktime", "audio/mp4", "audio/m4a", "audio/x-m4a":
		return len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp"))
	case "video/webm", "video/x-matroska", "audio/webm":
		return bytes.HasPrefix(data, []byte{0x1A, 0x45, 0xDF, 0xA3})
	case "audio/mpeg", "audio/mp3":
		return bytes.HasPrefix(data, []byte("ID3")) || (len(data) >= 2 && data[0] == 0xFF && data[1]&0xE0 == 0xE0)
	case "audio/wav", "audio/x-wav":
		return len(data) >= 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE"))
	case "audio/aac":
		return len(data) >= 2 && data[0] == 0xFF && data[1]&0xF0 == 0xF0
	case "audio/flac", "audio/x-flac":
		return bytes.HasPrefix(data, []byte("fLaC"))
	default:
		return false
	}
}

func (a *GeneratorApi) UploadReferenceVideo(c *gin.Context) {
	a.uploadReferenceMedia(c, "reference-videos", toolsService.GetMaxReferenceVideoSize(), allowedReferenceVideoTypes)
}

func (a *GeneratorApi) UploadReferenceAudio(c *gin.Context) {
	a.uploadReferenceMedia(c, "reference-audios", toolsService.GetMaxReferenceAudioSize(), allowedReferenceAudioTypes)
}

func (a *GeneratorApi) uploadReferenceMedia(c *gin.Context, kind string, maxSize int64, allowedTypes map[string]string) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		respondGeneratorHTTPError(c, http.StatusUnauthorized, "Unauthorized", "UNAUTHORIZED", true)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSize+referenceMediaMultipartOverheadBytes)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Missing file", "UPLOAD_FILE_MISSING", false)
		return
	}
	if fileHeader.Size > maxSize {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "File too large", "UPLOAD_FILE_TOO_LARGE", false)
		return
	}

	f, err := fileHeader.Open()
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to open file", "UPLOAD_OPEN_FAILED", false)
		return
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Failed to read file", "UPLOAD_READ_FAILED", false)
		return
	}
	if int64(len(data)) > maxSize {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "File too large", "UPLOAD_FILE_TOO_LARGE", false)
		return
	}

	contentType, ok := resolveUploadedContentType(data, fileHeader.Header.Get("Content-Type"), allowedTypes)
	if !ok {
		respondGeneratorHTTPError(c, http.StatusBadRequest, "Invalid file type", "UPLOAD_INVALID_FILE_TYPE", false)
		return
	}
	ext := allowedTypes[contentType]

	genService := service.GroupServiceApp.ToolServiceGroup.GeneratorService
	result, err := genService.UploadReferenceMedia(c.Request.Context(), uid, kind, contentType, data, ext)
	if err != nil {
		globals.Error(fmt.Sprintf("[GeneratorApi] Reference media upload failed (kind=%s user=%d): %v", kind, uid, err))
		respondGeneratorHTTPError(c, http.StatusInternalServerError, "Upload failed", "UPLOAD_FAILED", false)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    200,
		"message": "success",
		"data": UploadReferenceImageResponse{
			Success:       true,
			ID:            result.ID,
			URL:           result.URL,
			AssetID:       result.GlobalAssetID,
			GlobalAssetID: result.GlobalAssetID,
		},
	})
}
