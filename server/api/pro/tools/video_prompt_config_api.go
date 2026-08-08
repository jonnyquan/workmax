package tools

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"server/globals"
	"server/model"
	"server/model/common/response"
	"server/service/globalasset"
	"server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// VideoPromptConfigApi handles CRUD for user-saved video prompt builder
// configurations. Mirrors PromptBuilderApi's CRUD shape (list/get/save/update/
// delete), with video-specific validation for the adapter whitelist and the
// IR JSON pillar shape. Reuses writePromptBuilderHTTPError and isSafeHTTPURL
// from prompt_builder_api.go in the same package.
type VideoPromptConfigApi struct{}

var errVideoPromptQuotaExceeded = errors.New("video prompt builder quota exceeded")

const (
	maxVideoPromptNameLength      = 100
	maxVideoPromptThumbnailLength = 500
	maxVideoPromptAssetIDLength   = 80
	maxVideoPromptAssetURILength  = 500
	maxVideoPromptAssets          = 20
	maxVideoPromptReferenceImages = 8
	maxVideoPromptTimelineItems   = 20
	maxVideoPromptSubjectEntities = 10
	maxVideoPromptActionBeats     = 20
	maxVideoPromptDialogueLines   = 20
	maxVideoPromptShortTextRunes  = 240
	maxVideoPromptLongTextRunes   = 2000
	// IR JSON can carry rich timeline/audio/assets. 64KB is well above
	// the realistic payload (~5-10KB for a full IR) but protects the DB
	// text column from runaway inputs.
	maxVideoPromptIRJSONBytes      = 65536
	maxVideoPromptConfigBodyBytes  = maxVideoPromptIRJSONBytes + 8192
	maxVideoPromptConfigsPerUser   = 50
	videoPromptListDefaultPageSize = 20
	videoPromptListMaxPageSize     = 100
)

// videoPromptAllowedAdapters is the Server-owned catalog consumed by Desktop.
// Update this catalog and the Desktop contract together when an adapter ships.
var videoPromptAllowedAdapters = map[string]bool{
	"universal":     true,
	"sora-2":        true,
	"sora-2-pro":    true,
	"kling-2.6-pro": true,
	"veo-3":         true,
	"seedance-2":    true,
	"wan-2.1":       true,
	"hunyuan":       true,
}

var videoPromptAllowedAssetKinds = map[string]bool{
	"image":     true,
	"video":     true,
	"audio":     true,
	"character": true,
	"brand":     true,
	"product":   true,
}

var videoPromptAllowedAssetRoles = map[string]bool{
	"first-frame":      true,
	"last-frame":       true,
	"camera-movement":  true,
	"character-look":   true,
	"background-music": true,
	"voice-style":      true,
	"product-anchor":   true,
	"style-ref":        true,
}

var videoPromptAllowedDurations = map[int]bool{
	4: true, 5: true, 8: true, 10: true, 12: true, 16: true, 20: true,
}

var videoPromptAllowedAspectRatios = map[string]bool{
	"16:9": true, "9:16": true, "1:1": true, "4:5": true,
}

var videoPromptAllowedResolutions = map[string]bool{
	"720p": true, "1080p": true, "4k": true,
}

var videoPromptAllowedProductionLevels = map[string]bool{
	"raw": true, "amateur": true, "professional": true, "cinematic": true, "master": true,
}

var videoPromptAllowedTimeOfDay = map[string]bool{
	"dawn": true, "day": true, "dusk": true, "night": true, "golden-hour": true, "blue-hour": true,
}

var videoPromptAllowedActionIntensity = map[string]bool{
	"still": true, "subtle": true, "moderate": true, "dynamic": true, "extreme": true,
}

var videoPromptAllowedShotTypes = map[string]bool{
	"extreme-close-up": true, "close-up": true, "medium": true, "wide": true, "aerial": true, "pov": true, "over-the-shoulder": true, "dutch": true,
}

var videoPromptAllowedCameraAngles = map[string]bool{
	"eye-level": true, "low": true, "high": true, "overhead": true, "worms-eye": true,
}

var videoPromptAllowedCameraMovements = map[string]bool{
	"static": true, "dolly-in": true, "dolly-out": true, "pan-left": true, "pan-right": true, "whip-pan": true,
	"tilt-up": true, "tilt-down": true, "tracking": true, "orbit": true, "crane-up": true, "crane-down": true,
	"aerial": true, "handheld": true, "pov": true,
}

var videoPromptAllowedLightingTypes = map[string]bool{
	"natural": true, "studio": true, "practical": true, "mixed": true, "neon": true,
}

var videoPromptAllowedLightingQualities = map[string]bool{
	"hard": true, "soft": true, "diffuse": true,
}

var videoPromptAllowedLightingDirections = map[string]bool{
	"key-front": true, "side": true, "back-rim": true, "top": true, "bottom": true,
}

var videoPromptAllowedStylePresets = map[string]bool{
	"cinematic-realism": true, "anime-dreamscape": true, "found-footage-horror": true, "90s-sitcom": true,
	"music-video": true, "surreal": true, "ring-camera": true, "bodycam": true, "cctv": true,
	"dashboard-camera": true, "drone-aerial": true, "ugc-influencer": true, "auto": true,
}

func isVideoPromptAdapter(id string) bool {
	return videoPromptAllowedAdapters[strings.TrimSpace(id)]
}

// validateAndNormalizeVideoPromptIRJSON parses raw JSON text, verifies the
// Five-Pillar top-level shape, and returns the re-serialized compact form so
// the stored blob is normalized regardless of client whitespace.
func validateAndNormalizeVideoPromptIRJSON(raw string, adapter string, uid uint) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("ir is empty")
	}
	if len(raw) > maxVideoPromptIRJSONBytes {
		return "", errors.New("ir payload exceeds size limit")
	}
	var ir map[string]any
	if err := json.Unmarshal([]byte(raw), &ir); err != nil {
		return "", errors.New("ir is not valid JSON")
	}
	// Required Five-Pillar keys. Presence check only — the frontend's
	// ir-defaults.ts guarantees shape and the adapter render() is resilient
	// to missing nested fields, so we don't want to reimplement the full
	// TypeScript schema here.
	for _, k := range []string{"meta", "subject", "action", "environment", "cinematography", "style"} {
		if _, ok := ir[k]; !ok {
			return "", errors.New("ir is missing required pillar: " + k)
		}
	}
	meta, ok := ir["meta"].(map[string]any)
	if !ok {
		return "", errors.New("meta must be an object")
	}
	if adapter == "universal" {
		delete(meta, "targetModel")
	} else {
		meta["targetModel"] = adapter
	}
	durationSec, err := normalizeVideoPromptDuration(meta["durationSec"])
	if err != nil {
		return "", err
	}
	meta["durationSec"] = durationSec
	if err := normalizeVideoPromptMeta(meta); err != nil {
		return "", err
	}
	ir["meta"] = meta

	if timelineRaw, exists := ir["timeline"]; exists {
		normalizedTimeline, err := normalizeVideoPromptTimeline(timelineRaw, durationSec, uid)
		if err != nil {
			return "", err
		}
		if normalizedTimeline == nil {
			delete(ir, "timeline")
		} else {
			ir["timeline"] = normalizedTimeline
		}
	}
	if assetsRaw, exists := ir["assets"]; exists {
		normalizedAssets, err := normalizeVideoPromptAssetList(assetsRaw, maxVideoPromptAssets, uid)
		if err != nil {
			return "", err
		}
		if normalizedAssets == nil {
			delete(ir, "assets")
		} else {
			ir["assets"] = normalizedAssets
		}
	}
	if subjectRaw, exists := ir["subject"]; exists {
		subject, ok := subjectRaw.(map[string]any)
		if !ok {
			return "", errors.New("subject must be an object")
		}
		if err := normalizeVideoPromptSubject(subject); err != nil {
			return "", err
		}
		if consistencyRaw, exists := subject["consistency"]; exists {
			consistency, ok := consistencyRaw.(map[string]any)
			if !ok {
				return "", errors.New("subject.consistency must be an object")
			}
			if refsRaw, exists := consistency["referenceImages"]; exists {
				normalizedRefs, err := normalizeVideoPromptAssetList(refsRaw, maxVideoPromptReferenceImages, uid)
				if err != nil {
					return "", err
				}
				if normalizedRefs == nil {
					delete(consistency, "referenceImages")
				} else {
					consistency["referenceImages"] = normalizedRefs
				}
			}
			subject["consistency"] = consistency
		}
		ir["subject"] = subject
	}
	if actionRaw, exists := ir["action"]; exists {
		action, ok := actionRaw.(map[string]any)
		if !ok {
			return "", errors.New("action must be an object")
		}
		if err := normalizeVideoPromptAction(action); err != nil {
			return "", err
		}
		ir["action"] = action
	}
	if environmentRaw, exists := ir["environment"]; exists {
		environment, ok := environmentRaw.(map[string]any)
		if !ok {
			return "", errors.New("environment must be an object")
		}
		if err := normalizeVideoPromptEnvironment(environment); err != nil {
			return "", err
		}
		ir["environment"] = environment
	}
	if cinematographyRaw, exists := ir["cinematography"]; exists {
		cinematography, ok := cinematographyRaw.(map[string]any)
		if !ok {
			return "", errors.New("cinematography must be an object")
		}
		if err := normalizeVideoPromptCinematography(cinematography); err != nil {
			return "", err
		}
		ir["cinematography"] = cinematography
	}
	if styleRaw, exists := ir["style"]; exists {
		style, ok := styleRaw.(map[string]any)
		if !ok {
			return "", errors.New("style must be an object")
		}
		if err := normalizeVideoPromptStyle(style); err != nil {
			return "", err
		}
		ir["style"] = style
	}
	if audioRaw, exists := ir["audio"]; exists {
		audio, ok := audioRaw.(map[string]any)
		if !ok {
			return "", errors.New("audio must be an object")
		}
		if err := normalizeVideoPromptAudio(audio); err != nil {
			return "", err
		}
		ir["audio"] = audio
	}
	delete(ir, "edit")
	normalized, err := json.Marshal(ir)
	if err != nil {
		return "", errors.New("failed to normalize ir")
	}
	return string(normalized), nil
}

func normalizeVideoPromptAssetList(raw any, maxItems int, uid uint) ([]map[string]any, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("assets must be an array")
	}
	if len(list) == 0 {
		return nil, nil
	}
	if len(list) > maxItems {
		return nil, errors.New("too many assets")
	}
	normalized := make([]map[string]any, 0, len(list))
	for _, item := range list {
		asset, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("asset must be an object")
		}
		id, _ := asset["id"].(string)
		id = strings.TrimSpace(id)
		if id == "" || len(id) > maxVideoPromptAssetIDLength || strings.ContainsAny(id, "\r\n\t/\\") {
			return nil, errors.New("asset id is invalid")
		}
		kind, _ := asset["kind"].(string)
		kind = strings.TrimSpace(kind)
		if kind == "" || !videoPromptAllowedAssetKinds[kind] {
			return nil, errors.New("asset kind is invalid")
		}
		role, _ := asset["role"].(string)
		role = strings.TrimSpace(role)
		if role == "" || !videoPromptAllowedAssetRoles[role] {
			return nil, errors.New("asset role is invalid")
		}
		if rawURI, exists := asset["uri"]; exists {
			uri, ok := rawURI.(string)
			if !ok {
				return nil, errors.New("asset uri must be a string")
			}
			uri = strings.TrimSpace(uri)
			if uri == "" {
				delete(asset, "uri")
			} else {
				if len(uri) > maxVideoPromptAssetURILength || !isAllowedVideoPromptAssetURL(uri, uid) {
					return nil, errors.New("asset uri must be an uploaded platform asset URL")
				}
				asset["uri"] = uri
			}
		}
		if err := normalizeVideoPromptAssetGlobalID(asset, uid); err != nil {
			return nil, err
		}
		asset["id"] = id
		asset["kind"] = kind
		asset["role"] = role
		normalized = append(normalized, asset)
	}
	return normalized, nil
}

func normalizeVideoPromptAssetGlobalID(asset map[string]any, uid uint) error {
	raw, exists := asset["assetId"]
	if !exists {
		raw = asset["globalAssetId"]
	}
	assetID := parsePositiveUint(raw)
	if assetID == 0 {
		delete(asset, "assetId")
		delete(asset, "globalAssetId")
		return nil
	}
	db := globals.GraDBs["system"]
	if db == nil {
		return errors.New("global asset repository is unavailable")
	}
	loaded, err := globalasset.NewRepository(db).LoadForAccess(uint(assetID), int(uid))
	if err != nil {
		return errors.New("asset id is not accessible")
	}
	if !videoPromptAssetKindMatchesGlobal(asset, loaded) {
		return errors.New("asset id kind does not match asset kind")
	}
	asset["assetId"] = loaded.Id
	asset["globalAssetId"] = loaded.Id
	if strings.TrimSpace(asString(asset["uri"])) == "" && strings.TrimSpace(loaded.URL) != "" {
		asset["uri"] = strings.TrimSpace(loaded.URL)
	}
	return nil
}

func videoPromptAssetKindMatchesGlobal(asset map[string]any, loaded *model.GlobalAsset) bool {
	if loaded == nil {
		return false
	}
	kind := strings.TrimSpace(asString(asset["kind"]))
	if kind == "" {
		return false
	}
	switch kind {
	case "image", "video", "audio":
		return strings.EqualFold(loaded.Kind, kind)
	case "character", "brand", "product":
		// These slots are semantic references. During the migration they may
		// still point at an uploaded image that represents the entity.
		return strings.EqualFold(loaded.Kind, model.GlobalAssetKindImage)
	default:
		return false
	}
}

func parsePositiveUint(raw any) uint64 {
	switch value := raw.(type) {
	case float64:
		if value > 0 && math.Trunc(value) == value {
			return uint64(value)
		}
	case int:
		if value > 0 {
			return uint64(value)
		}
	case uint:
		if value > 0 {
			return uint64(value)
		}
	case string:
		parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

func asString(raw any) string {
	value, _ := raw.(string)
	return value
}

func isAllowedVideoPromptAssetURL(raw string, uid uint) bool {
	if uid == 0 || !isSafeHTTPURL(raw) {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.User != nil {
		return false
	}
	if !isAllowedVideoReferenceMediaHost(parsed.Hostname()) {
		return false
	}
	pathValue := parsed.EscapedPath()
	if pathValue == "" {
		pathValue = parsed.Path
	}
	if pathValue == "" || hasUnsafeURLPath(pathValue) {
		return false
	}
	uidSegment := strconv.FormatUint(uint64(uid), 10)
	allowedPathMarkers := []string{
		"/uploads/references/uid/" + uidSegment + "/",
		"/references/uid/" + uidSegment + "/",
		"/uploads/generations/reference-images/" + uidSegment + "/",
		"/generations/reference-images/" + uidSegment + "/",
		"/reference-images/" + uidSegment + "/",
		"/uploads/generations/uid/" + uidSegment + "/",
		"/generations/uid/" + uidSegment + "/",
		"/reference-videos/uid/" + uidSegment + "/",
		"/reference-audios/uid/" + uidSegment + "/",
		"/uploads/canvas/uid/" + uidSegment + "/",
		"/canvas/uid/" + uidSegment + "/",
		"/audio/uid/" + uidSegment + "/",
	}
	for _, marker := range allowedPathMarkers {
		if strings.Contains(pathValue, marker) {
			return true
		}
	}
	return false
}

func trimVideoPromptString(obj map[string]any, field string, maxRunes int, required bool) error {
	raw, exists := obj[field]
	if !exists {
		if required {
			return errors.New(field + " is required")
		}
		return nil
	}
	value, ok := raw.(string)
	if !ok {
		return errors.New(field + " must be a string")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return errors.New(field + " is required")
		}
		delete(obj, field)
		return nil
	}
	if len([]rune(value)) > maxRunes {
		return errors.New(field + " is too long")
	}
	obj[field] = value
	return nil
}

func trimVideoPromptDraftString(obj map[string]any, field string, maxRunes int) error {
	raw, exists := obj[field]
	if !exists {
		return nil
	}
	value, ok := raw.(string)
	if !ok {
		return errors.New(field + " must be a string")
	}
	value = strings.TrimSpace(value)
	if len([]rune(value)) > maxRunes {
		return errors.New(field + " is too long")
	}
	obj[field] = value
	return nil
}

func normalizeVideoPromptStringEnum(obj map[string]any, field string, allowed map[string]bool, required bool) error {
	raw, exists := obj[field]
	if !exists {
		if required {
			return errors.New(field + " is required")
		}
		return nil
	}
	value, ok := raw.(string)
	if !ok {
		return errors.New(field + " must be a string")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return errors.New(field + " is required")
		}
		delete(obj, field)
		return nil
	}
	if !allowed[value] {
		return errors.New(field + " is invalid")
	}
	obj[field] = value
	return nil
}

func normalizeVideoPromptStringArray(obj map[string]any, field string, maxItems int, maxRunes int) error {
	raw, exists := obj[field]
	if !exists {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return errors.New(field + " must be an array")
	}
	if len(list) == 0 {
		delete(obj, field)
		return nil
	}
	if len(list) > maxItems {
		return errors.New(field + " has too many items")
	}
	next := make([]string, 0, len(list))
	for _, item := range list {
		value, ok := item.(string)
		if !ok {
			return errors.New(field + " items must be strings")
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len([]rune(value)) > maxRunes {
			return errors.New(field + " item is too long")
		}
		next = append(next, value)
	}
	if len(next) == 0 {
		delete(obj, field)
		return nil
	}
	obj[field] = next
	return nil
}

func normalizeVideoPromptMeta(meta map[string]any) error {
	duration, ok := meta["durationSec"].(int)
	if !ok || !videoPromptAllowedDurations[duration] {
		return errors.New("meta.durationSec is invalid")
	}
	if err := trimVideoPromptString(meta, "title", maxVideoPromptShortTextRunes, false); err != nil {
		return errors.New("meta." + err.Error())
	}
	if err := normalizeVideoPromptStringEnum(meta, "aspectRatio", videoPromptAllowedAspectRatios, true); err != nil {
		return errors.New("meta." + err.Error())
	}
	if err := normalizeVideoPromptStringEnum(meta, "resolution", videoPromptAllowedResolutions, true); err != nil {
		return errors.New("meta." + err.Error())
	}
	if err := normalizeVideoPromptStringEnum(meta, "productionLevel", videoPromptAllowedProductionLevels, true); err != nil {
		return errors.New("meta." + err.Error())
	}
	return nil
}

func normalizeVideoPromptSubject(subject map[string]any) error {
	entitiesRaw, exists := subject["entities"]
	if !exists {
		return errors.New("subject.entities is required")
	}
	entities, ok := entitiesRaw.([]any)
	if !ok {
		return errors.New("subject.entities must be an array")
	}
	if len(entities) > maxVideoPromptSubjectEntities {
		return errors.New("subject.entities has too many items")
	}
	for _, rawEntity := range entities {
		entity, ok := rawEntity.(map[string]any)
		if !ok {
			return errors.New("subject entity must be an object")
		}
		if err := trimVideoPromptString(entity, "id", maxVideoPromptAssetIDLength, true); err != nil {
			return errors.New("subject.entity." + err.Error())
		}
		if err := trimVideoPromptString(entity, "name", maxVideoPromptShortTextRunes, false); err != nil {
			return errors.New("subject.entity." + err.Error())
		}
		if err := trimVideoPromptDraftString(entity, "descriptor", maxVideoPromptLongTextRunes); err != nil {
			return errors.New("subject.entity." + err.Error())
		}
		if err := normalizeVideoPromptStringArray(entity, "appearance", 20, maxVideoPromptShortTextRunes); err != nil {
			return errors.New("subject.entity." + err.Error())
		}
		if err := normalizeVideoPromptStringArray(entity, "wardrobe", 20, maxVideoPromptShortTextRunes); err != nil {
			return errors.New("subject.entity." + err.Error())
		}
	}
	consistencyRaw, exists := subject["consistency"]
	if !exists {
		return errors.New("subject.consistency is required")
	}
	consistency, ok := consistencyRaw.(map[string]any)
	if !ok {
		return errors.New("subject.consistency must be an object")
	}
	if rawLock, exists := consistency["lockAppearance"]; exists {
		if _, ok := rawLock.(bool); !ok {
			return errors.New("subject.consistency.lockAppearance must be a boolean")
		}
	}
	if err := normalizeVideoPromptStringArray(consistency, "loraIds", 10, maxVideoPromptAssetIDLength); err != nil {
		return errors.New("subject.consistency." + err.Error())
	}
	if err := normalizeVideoPromptStringArray(consistency, "characterRefIds", 10, maxVideoPromptAssetIDLength); err != nil {
		return errors.New("subject.consistency." + err.Error())
	}
	subject["consistency"] = consistency
	return nil
}

func normalizeVideoPromptAction(action map[string]any) error {
	if err := normalizeVideoPromptStringEnum(action, "intensity", videoPromptAllowedActionIntensity, true); err != nil {
		return errors.New("action." + err.Error())
	}
	if err := trimVideoPromptString(action, "primaryMotion", maxVideoPromptLongTextRunes, false); err != nil {
		return errors.New("action." + err.Error())
	}
	beatsRaw, exists := action["beats"]
	if !exists {
		return errors.New("action.beats is required")
	}
	beats, ok := beatsRaw.([]any)
	if !ok {
		return errors.New("action.beats must be an array")
	}
	if len(beats) > maxVideoPromptActionBeats {
		return errors.New("action.beats has too many items")
	}
	for _, rawBeat := range beats {
		beat, ok := rawBeat.(map[string]any)
		if !ok {
			return errors.New("action beat must be an object")
		}
		if err := trimVideoPromptString(beat, "id", maxVideoPromptAssetIDLength, true); err != nil {
			return errors.New("action.beat." + err.Error())
		}
		if err := trimVideoPromptDraftString(beat, "description", maxVideoPromptLongTextRunes); err != nil {
			return errors.New("action.beat." + err.Error())
		}
	}
	return nil
}

func normalizeVideoPromptEnvironment(environment map[string]any) error {
	if err := trimVideoPromptDraftString(environment, "setting", maxVideoPromptLongTextRunes); err != nil {
		return errors.New("environment." + err.Error())
	}
	if err := normalizeVideoPromptStringEnum(environment, "timeOfDay", videoPromptAllowedTimeOfDay, true); err != nil {
		return errors.New("environment." + err.Error())
	}
	if err := trimVideoPromptString(environment, "weather", maxVideoPromptShortTextRunes, false); err != nil {
		return errors.New("environment." + err.Error())
	}
	if err := trimVideoPromptDraftString(environment, "atmosphere", maxVideoPromptLongTextRunes); err != nil {
		return errors.New("environment." + err.Error())
	}
	if err := trimVideoPromptString(environment, "backgroundMotion", maxVideoPromptLongTextRunes, false); err != nil {
		return errors.New("environment." + err.Error())
	}
	if err := normalizeVideoPromptStringArray(environment, "props", 30, maxVideoPromptShortTextRunes); err != nil {
		return errors.New("environment." + err.Error())
	}
	return nil
}

func normalizeVideoPromptCinematography(cinematography map[string]any) error {
	if err := normalizeVideoPromptStringEnum(cinematography, "shotType", videoPromptAllowedShotTypes, true); err != nil {
		return errors.New("cinematography." + err.Error())
	}
	if err := normalizeVideoPromptStringEnum(cinematography, "angle", videoPromptAllowedCameraAngles, true); err != nil {
		return errors.New("cinematography." + err.Error())
	}
	if _, hasCameraMovement := cinematography["cameraMovement"]; !hasCameraMovement {
		if legacyMovement, hasLegacyMovement := cinematography["movement"]; hasLegacyMovement {
			cinematography["cameraMovement"] = legacyMovement
			delete(cinematography, "movement")
		}
	}
	if err := normalizeVideoPromptStringEnum(cinematography, "cameraMovement", videoPromptAllowedCameraMovements, true); err != nil {
		return errors.New("cinematography." + err.Error())
	}
	lightingRaw, exists := cinematography["lighting"]
	if !exists {
		return errors.New("cinematography.lighting is required")
	}
	lighting, ok := lightingRaw.(map[string]any)
	if !ok {
		return errors.New("cinematography.lighting must be an object")
	}
	if err := normalizeVideoPromptStringEnum(lighting, "type", videoPromptAllowedLightingTypes, true); err != nil {
		return errors.New("cinematography.lighting." + err.Error())
	}
	if err := normalizeVideoPromptStringEnum(lighting, "quality", videoPromptAllowedLightingQualities, true); err != nil {
		return errors.New("cinematography.lighting." + err.Error())
	}
	if err := normalizeVideoPromptStringEnum(lighting, "direction", videoPromptAllowedLightingDirections, true); err != nil {
		return errors.New("cinematography.lighting." + err.Error())
	}
	if err := normalizeVideoPromptStringArray(lighting, "colorAnchors", 12, 80); err != nil {
		return errors.New("cinematography.lighting." + err.Error())
	}
	if rawTemperature, exists := lighting["temperatureK"]; exists {
		temperature, ok := rawTemperature.(float64)
		if !ok || temperature < 1000 || temperature > 20000 {
			return errors.New("cinematography.lighting.temperatureK is invalid")
		}
		lighting["temperatureK"] = int(math.Round(temperature))
	}
	cinematography["lighting"] = lighting
	return nil
}

func normalizeVideoPromptStyle(style map[string]any) error {
	if err := normalizeVideoPromptStringEnum(style, "preset", videoPromptAllowedStylePresets, false); err != nil {
		return errors.New("style." + err.Error())
	}
	if err := trimVideoPromptString(style, "filmReference", maxVideoPromptShortTextRunes, false); err != nil {
		return errors.New("style." + err.Error())
	}
	if err := trimVideoPromptString(style, "directorReference", maxVideoPromptShortTextRunes, false); err != nil {
		return errors.New("style." + err.Error())
	}
	return nil
}

func normalizeVideoPromptAudio(audio map[string]any) error {
	if err := normalizeVideoPromptStringArray(audio, "backgroundSound", 20, maxVideoPromptShortTextRunes); err != nil {
		return errors.New("audio." + err.Error())
	}
	if err := normalizeVideoPromptStringArray(audio, "sfx", 20, maxVideoPromptShortTextRunes); err != nil {
		return errors.New("audio." + err.Error())
	}
	dialogueRaw, exists := audio["dialogue"]
	if exists {
		dialogue, ok := dialogueRaw.([]any)
		if !ok {
			return errors.New("audio.dialogue must be an array")
		}
		if len(dialogue) > maxVideoPromptDialogueLines {
			return errors.New("audio.dialogue has too many items")
		}
		for _, rawLine := range dialogue {
			line, ok := rawLine.(map[string]any)
			if !ok {
				return errors.New("audio dialogue line must be an object")
			}
			if err := trimVideoPromptDraftString(line, "speaker", maxVideoPromptShortTextRunes); err != nil {
				return errors.New("audio.dialogue." + err.Error())
			}
			if err := trimVideoPromptDraftString(line, "text", maxVideoPromptLongTextRunes); err != nil {
				return errors.New("audio.dialogue." + err.Error())
			}
			if err := trimVideoPromptString(line, "emotion", maxVideoPromptShortTextRunes, false); err != nil {
				return errors.New("audio.dialogue." + err.Error())
			}
		}
	}
	return nil
}

func normalizeVideoPromptDuration(raw any) (int, error) {
	value, ok := raw.(float64)
	if !ok || value <= 0 {
		return 0, errors.New("meta.durationSec must be a positive number")
	}
	duration := int(math.Round(value))
	if duration <= 0 {
		return 0, errors.New("meta.durationSec must be a positive number")
	}
	return duration, nil
}

func normalizeVideoPromptTimeline(raw any, durationSec int, uid uint) ([]map[string]any, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("timeline must be an array")
	}
	if len(list) == 0 {
		return nil, nil
	}
	if len(list) > maxVideoPromptTimelineItems {
		return nil, errors.New("timeline has too many items")
	}

	normalized := make([]map[string]any, 0, len(list))
	prevEnd := 0.0
	maxDuration := float64(durationSec)

	for index, item := range list {
		segment, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("timeline item must be an object")
		}
		start, ok := segment["start"].(float64)
		if !ok {
			return nil, errors.New("timeline start must be a number")
		}
		end, ok := segment["end"].(float64)
		if !ok {
			return nil, errors.New("timeline end must be a number")
		}

		start = math.Max(0, math.Min(maxDuration, start))
		end = math.Max(0, math.Min(maxDuration, end))
		if index > 0 && start < prevEnd {
			start = prevEnd
		}
		if end < start {
			end = start
		}
		if err := trimVideoPromptDraftString(segment, "content", maxVideoPromptLongTextRunes); err != nil {
			return nil, errors.New("timeline." + err.Error())
		}
		if err := normalizeVideoPromptStringEnum(segment, "cameraOverride", videoPromptAllowedCameraMovements, false); err != nil {
			return nil, errors.New("timeline." + err.Error())
		}
		if err := normalizeVideoPromptStringEnum(segment, "transition", map[string]bool{
			"cut": true, "dissolve": true, "match-cut": true, "none": true,
		}, false); err != nil {
			return nil, errors.New("timeline." + err.Error())
		}
		if assetsRaw, exists := segment["assets"]; exists {
			normalizedAssets, err := normalizeVideoPromptAssetList(assetsRaw, maxVideoPromptAssets, uid)
			if err != nil {
				return nil, err
			}
			if normalizedAssets == nil {
				delete(segment, "assets")
			} else {
				segment["assets"] = normalizedAssets
			}
		}

		segment["start"] = roundVideoPromptTimelineValue(start)
		segment["end"] = roundVideoPromptTimelineValue(end)
		prevEnd = end
		normalized = append(normalized, segment)
	}

	return normalized, nil
}

func roundVideoPromptTimelineValue(value float64) float64 {
	return math.Round(value*10) / 10
}

// videoPromptConfigSummary is the lightweight row shape returned by
// ListConfigs. The heavy IR JSON is excluded so list responses stay small
// even when a user has 50 configs.
type videoPromptConfigSummary struct {
	ID         uint   `json:"id" gorm:"column:id"`
	UID        uint   `json:"uid" gorm:"column:uid"`
	Name       string `json:"name" gorm:"column:name"`
	Adapter    string `json:"adapter" gorm:"column:adapter"`
	Thumbnail  string `json:"thumbnail" gorm:"column:thumbnail"`
	IsFavorite bool   `json:"isFavorite" gorm:"column:is_favorite"`
	CreatedAt  string `json:"createdAt" gorm:"column:created_at"`
	UpdatedAt  string `json:"updatedAt" gorm:"column:updated_at"`
}

type saveVideoPromptConfigRequest struct {
	Name       string  `json:"name"`
	Adapter    string  `json:"adapter"`
	IRJSON     string  `json:"irJSON"`
	Thumbnail  *string `json:"thumbnail"`
	IsFavorite *bool   `json:"isFavorite"`
}

func (a *VideoPromptConfigApi) ListConfigs(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", strconv.Itoa(videoPromptListDefaultPageSize)))
	if pageSize < 1 || pageSize > videoPromptListMaxPageSize {
		pageSize = videoPromptListDefaultPageSize
	}

	db := globals.GraDBs["system"].Model(&model.VideoPromptConfig{}).Where("uid = ?", uid)

	var total int64
	if err := db.Count(&total).Error; err != nil {
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Failed to count configs", nil)
		return
	}

	var configs []videoPromptConfigSummary
	if err := db.
		Select("id, uid, name, adapter, thumbnail, is_favorite, created_at, updated_at").
		Order("updated_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&configs).Error; err != nil {
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Failed to list configs", nil)
		return
	}

	response.OkWithData(map[string]interface{}{
		"list":     configs,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	}, c)
}

func (a *VideoPromptConfigApi) GetConfig(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid config id", nil)
		return
	}

	var config model.VideoPromptConfig
	if err := globals.GraDBs["system"].
		Where("id = ? AND uid = ?", id, uid).
		First(&config).Error; err != nil {
		writePromptBuilderHTTPError(c, http.StatusNotFound, "Config not found", nil)
		return
	}

	response.OkWithData(config, c)
}

func (a *VideoPromptConfigApi) SaveConfig(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	var req saveVideoPromptConfigRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxVideoPromptConfigBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), nil)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Config name is required", nil)
		return
	}
	if len(name) > maxVideoPromptNameLength {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Config name is too long", nil)
		return
	}

	adapter := strings.TrimSpace(req.Adapter)
	if !isVideoPromptAdapter(adapter) {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid adapter", nil)
		return
	}

	thumbnail := ""
	if req.Thumbnail != nil {
		thumbnail = strings.TrimSpace(*req.Thumbnail)
	}
	if len(thumbnail) > maxVideoPromptThumbnailLength {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Thumbnail is too long", nil)
		return
	}
	if thumbnail != "" && !isSafeHTTPURL(thumbnail) {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Thumbnail must be an http(s) URL", nil)
		return
	}

	normalizedIR, err := validateAndNormalizeVideoPromptIRJSON(req.IRJSON, adapter, uid)
	if err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid ir JSON: "+err.Error(), nil)
		return
	}

	config := model.VideoPromptConfig{
		UID:        uid,
		Name:       name,
		Adapter:    adapter,
		IRJSON:     normalizedIR,
		Thumbnail:  thumbnail,
		IsFavorite: req.IsFavorite != nil && *req.IsFavorite,
	}

	// Per-user quota + insert in one tx with a next-key lock on uid range of
	// idx_vp_uid. Same pattern as PromptBuilderApi.SaveConfig — blocks
	// concurrent inserts so two parallel saves cannot both observe count ==
	// maxVideoPromptConfigsPerUser-1 and race past the quota cap.
	err = globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.VideoPromptConfig{}).
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("uid = ?", uid).
			Count(&count).Error; err != nil {
			return err
		}
		if count >= maxVideoPromptConfigsPerUser {
			return errVideoPromptQuotaExceeded
		}
		return tx.Create(&config).Error
	})
	if err != nil {
		if errors.Is(err, errVideoPromptQuotaExceeded) {
			writePromptBuilderHTTPError(c, http.StatusTooManyRequests, "Maximum saved configurations reached (50)", nil)
			return
		}
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Failed to save config", nil)
		return
	}

	response.OkWithData(config, c)
}

func (a *VideoPromptConfigApi) UpdateConfig(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid config id", nil)
		return
	}

	var req saveVideoPromptConfigRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxVideoPromptConfigBodyBytes)
	if err := c.ShouldBindJSON(&req); err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid request: "+err.Error(), nil)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Config name is required", nil)
		return
	}
	if len(name) > maxVideoPromptNameLength {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Config name is too long", nil)
		return
	}

	adapter := strings.TrimSpace(req.Adapter)
	if !isVideoPromptAdapter(adapter) {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid adapter", nil)
		return
	}

	thumbnail := ""
	if req.Thumbnail != nil {
		thumbnail = strings.TrimSpace(*req.Thumbnail)
	}
	if len(thumbnail) > maxVideoPromptThumbnailLength {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Thumbnail is too long", nil)
		return
	}
	if thumbnail != "" && !isSafeHTTPURL(thumbnail) {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Thumbnail must be an http(s) URL", nil)
		return
	}

	normalizedIR, err := validateAndNormalizeVideoPromptIRJSON(req.IRJSON, adapter, uid)
	if err != nil {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid ir JSON: "+err.Error(), nil)
		return
	}

	// Lock → update → re-read pattern matches PromptBuilderApi.UpdateConfig.
	// Guarantees the returned row reflects this tx's write even under
	// concurrent updates on the same row.
	var updated model.VideoPromptConfig
	err = globals.GraDBs["system"].Transaction(func(tx *gorm.DB) error {
		var existing model.VideoPromptConfig
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND uid = ?", id, uid).
			First(&existing).Error; err != nil {
			return err
		}
		updates := map[string]interface{}{
			"name":    name,
			"adapter": adapter,
			"ir_json": normalizedIR,
		}
		if req.Thumbnail != nil {
			updates["thumbnail"] = thumbnail
		}
		if req.IsFavorite != nil {
			updates["is_favorite"] = *req.IsFavorite
		}
		if err := tx.Model(&existing).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).First(&updated).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writePromptBuilderHTTPError(c, http.StatusNotFound, "Config not found", nil)
			return
		}
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Failed to update config", nil)
		return
	}
	response.OkWithData(updated, c)
}

func (a *VideoPromptConfigApi) DeleteConfig(c *gin.Context) {
	uid := utils.GetUserID(c)
	if uid == 0 {
		writePromptBuilderHTTPError(c, http.StatusUnauthorized, "Unauthorized", nil)
		return
	}

	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		writePromptBuilderHTTPError(c, http.StatusBadRequest, "Invalid config id", nil)
		return
	}

	res := globals.GraDBs["system"].
		Where("id = ? AND uid = ?", id, uid).
		Delete(&model.VideoPromptConfig{})
	if res.Error != nil {
		writePromptBuilderHTTPError(c, http.StatusInternalServerError, "Failed to delete config", nil)
		return
	}
	if res.RowsAffected == 0 {
		writePromptBuilderHTTPError(c, http.StatusNotFound, "Config not found", nil)
		return
	}

	response.Ok(c)
}
