package canvas

import (
	"fmt"
	"math"

	"server/model"
)

// document_v2_validate.go is the runtime shape check for v2 documents
// (§8.3). It is intentionally pragmatic: it verifies the structural
// invariants the migrator / server code relies on, not every field's
// business semantics. Deeper validation (timeline math, asset id
// existence) lives in the handlers that actually use those fields.

// ErrCodeSchemaInvalid is surfaced to clients when a document fails the
// structural check — distinct from ErrCodeSchemaUnsupported which
// signals a version mismatch.
const ErrCodeSchemaInvalid = "CANVAS_SCHEMA_INVALID"

var allowedViewModes = map[string]struct{}{
	string(ViewModeFreeform):  {},
	string(ViewModeShotBoard): {},
	string(ViewModeTimeline):  {},
	string(ViewModeWorkflow):  {},
}

var allowedTrackKinds = map[string]struct{}{
	string(TimelineTrackVideo):    {},
	string(TimelineTrackAudio):    {},
	string(TimelineTrackSubtitle): {},
}

var allowedAssetScopes = map[string]struct{}{
	string(AssetScopeElement): {},
	string(AssetScopeShot):    {},
	string(AssetScopeProject): {},
}

var allowedElementTypes = map[string]struct{}{
	"text":    {},
	"image":   {},
	"shape":   {},
	"drawing": {},
	"frame":   {},
	"widget":  {},
	"video":   {},
}

var allowedLayerRoles = map[string]struct{}{
	"source":     {},
	"background": {},
	"foreground": {},
	"mask":       {},
	"composite":  {},
}

var allowedWorkflowNodeKinds = map[string]struct{}{
	"input":     {},
	"operation": {},
	"model":     {},
	"output":    {},
}

var allowedWorkflowOperations = map[string]struct{}{
	"upscale":        {},
	"remove-bg":      {},
	"img2img":        {},
	"outpaint":       {},
	"inpaint":        {},
	"mockup":         {},
	"edit-text":      {},
	"split-layers":   {},
	"generate":       {},
	"generate-video": {},
}

var allowedWorkflowRunStatuses = map[string]struct{}{
	string(WorkflowRunQueued):    {},
	string(WorkflowRunRunning):   {},
	string(WorkflowRunCompleted): {},
	string(WorkflowRunFailed):    {},
	string(WorkflowRunCancelled): {},
}

var allowedWorkflowNodeRunStatuses = map[string]struct{}{
	string(WorkflowNodeRunIdle):      {},
	string(WorkflowNodeRunQueued):    {},
	string(WorkflowNodeRunRunning):   {},
	string(WorkflowNodeRunBlocked):   {},
	string(WorkflowNodeRunCompleted): {},
	string(WorkflowNodeRunFailed):    {},
	string(WorkflowNodeRunSkipped):   {},
}

var allowedWorkflowEditedBy = map[string]struct{}{
	"user":   {},
	"agent":  {},
	"system": {},
}

const (
	maxCanvasElementIDLength     = 128
	maxCanvasElementKindLength   = 64
	maxCanvasElementStringLength = 8192
	maxCanvasElementPathLength   = 64 << 10
	maxCanvasElementDimension    = 1_000_000
	maxCanvasElementCoordinate   = 10_000_000
	maxCanvasWorkflowNodes       = 200
	maxCanvasWorkflowEdges       = 400
	maxCanvasWorkflowRuns        = 5
	maxCanvasWorkflowParamsKeys  = 64
	maxCanvasWorkflowParamDepth  = 4
)

// ValidateCanvasDocumentV2 reports the first structural violation it
// finds in `doc`. The caller is expected to have already routed by
// schemaVersion via EnsureSchemaVersion.
func ValidateCanvasDocumentV2(doc model.JSONMap) error {
	if doc == nil {
		return newSchemaInvalid("document is nil")
	}

	if err := requireSchemaVersion(doc, SchemaVersionV2); err != nil {
		return err
	}

	elements, err := requireArray(doc, "elements")
	if err != nil {
		return err
	}
	for i, raw := range elements {
		elem, ok := raw.(map[string]interface{})
		if !ok {
			return newSchemaInvalid(fmt.Sprintf("elements[%d] must be an object", i))
		}
		if err := validateElement(elem, i); err != nil {
			return err
		}
	}

	viewport, ok := asStringMap(doc["viewport"])
	if !ok {
		return newSchemaInvalid("viewport must be an object")
	}
	for _, key := range []string{"x", "y", "scale"} {
		if _, err := readNumber(viewport, key); err != nil {
			return newSchemaInvalid(fmt.Sprintf("viewport.%s must be a number", key))
		}
	}

	mode, ok := doc["viewMode"].(string)
	if !ok {
		return newSchemaInvalid("viewMode must be a string")
	}
	if _, ok := allowedViewModes[mode]; !ok {
		return newSchemaInvalid(fmt.Sprintf("viewMode %q is not supported", mode))
	}

	if selectedIDs, present := doc["selectedIds"]; present && selectedIDs != nil {
		var selectedList []string
		switch v := selectedIDs.(type) {
		case []interface{}:
			for i, raw := range v {
				id, ok := raw.(string)
				if !ok {
					return newSchemaInvalid(fmt.Sprintf("selectedIds[%d] must be a string", i))
				}
				selectedList = append(selectedList, id)
			}
		case []string:
			selectedList = v
		default:
			return newSchemaInvalid("selectedIds must be an array")
		}
		for i, id := range selectedList {
			if err := validateCanvasElementIdentifier(id, fmt.Sprintf("selectedIds[%d]", i)); err != nil {
				return err
			}
		}
	}

	if settings, present := doc["settings"]; present && settings != nil {
		if _, ok := asStringMap(settings); !ok {
			return newSchemaInvalid("settings must be an object")
		}
	}

	if shots, present := doc["shots"]; present && shots != nil {
		shotList, ok := shots.([]interface{})
		if !ok {
			return newSchemaInvalid("shots must be an array")
		}
		for i, raw := range shotList {
			shot, ok := raw.(map[string]interface{})
			if !ok {
				return newSchemaInvalid(fmt.Sprintf("shots[%d] must be an object", i))
			}
			if err := validateShot(shot, i); err != nil {
				return err
			}
		}
	}

	if timeline, present := doc["timeline"]; present && timeline != nil {
		tl, ok := asStringMap(timeline)
		if !ok {
			return newSchemaInvalid("timeline must be an object")
		}
		if err := validateTimeline(tl); err != nil {
			return err
		}
	}

	if workflow, present := doc["workflow"]; present && workflow != nil {
		wf, ok := asStringMap(workflow)
		if !ok {
			return newSchemaInvalid("workflow must be an object")
		}
		if err := validateWorkflow(wf); err != nil {
			return err
		}
	}

	if workflowRuns, present := doc["workflowRuns"]; present && workflowRuns != nil {
		runs, ok := normalizeInterfaceSlice(workflowRuns)
		if !ok {
			return newSchemaInvalid("workflowRuns must be an array")
		}
		if err := validateWorkflowRuns(runs); err != nil {
			return err
		}
	}

	if pb, present := doc["projectAssetBindings"]; present && pb != nil {
		binding, ok := asStringMap(pb)
		if !ok {
			return newSchemaInvalid("projectAssetBindings must be an object")
		}
		if err := validateAssetBinding(binding, "projectAssetBindings"); err != nil {
			return err
		}
	}

	return nil
}

// NormalizeCanvasDocumentForStorage is the write-path gate: migrate the
// caller's document to the current schema and reject structurally invalid v2
// payloads before they can be persisted.
func NormalizeCanvasDocumentForStorage(doc model.JSONMap) (model.JSONMap, error) {
	migrated, err := EnsureSchemaVersion(doc)
	if err != nil {
		return migrated, err
	}
	if err := ValidateCanvasDocumentV2(migrated); err != nil {
		return migrated, err
	}
	return migrated, nil
}

func validateElement(elem map[string]interface{}, idx int) error {
	id, ok := elem["id"].(string)
	if !ok {
		return newSchemaInvalid(fmt.Sprintf("elements[%d].id must be a string", idx))
	}
	if err := validateCanvasElementIdentifier(id, fmt.Sprintf("elements[%d].id", idx)); err != nil {
		return err
	}
	// v1 elements keep their `type` field; v2-native elements may also
	// carry `kind` and `typeName`. Either is acceptable, but one of the
	// two string keys must be present so the renderer can dispatch.
	typeName, hasType := elem["type"].(string)
	kind, hasKind := elem["kind"].(string)
	if !hasType && !hasKind {
		return newSchemaInvalid(fmt.Sprintf("elements[%d] requires type or kind", idx))
	}
	if hasType {
		if _, ok := allowedElementTypes[typeName]; !ok {
			return newSchemaInvalid(fmt.Sprintf("elements[%d].type %q is not supported", idx, typeName))
		}
		for _, key := range []string{"x", "y"} {
			value, err := readNumber(elem, key)
			if err != nil || math.Abs(value) > maxCanvasElementCoordinate {
				return newSchemaInvalid(fmt.Sprintf("elements[%d].%s must be a finite number", idx, key))
			}
		}
	}
	if hasKind {
		if kind == "" || len([]rune(kind)) > maxCanvasElementKindLength || hasControlChars(kind) {
			return newSchemaInvalid(fmt.Sprintf("elements[%d].kind is invalid", idx))
		}
	}
	for _, key := range []string{"width", "height"} {
		if raw, present := elem[key]; present && raw != nil {
			value, err := coerceNumber(raw)
			if err != nil || value < 0 || value > maxCanvasElementDimension {
				return newSchemaInvalid(fmt.Sprintf("elements[%d].%s must be a valid dimension", idx, key))
			}
		}
	}
	for _, key := range []string{"rotation", "strokeWidth", "fontSize", "lineHeight", "timelineDurationSeconds", "videoDuration", "startFrameWeight", "endFrameWeight"} {
		if raw, present := elem[key]; present && raw != nil {
			value, err := coerceNumber(raw)
			if err != nil || value < -maxCanvasElementCoordinate || value > maxCanvasElementCoordinate {
				return newSchemaInvalid(fmt.Sprintf("elements[%d].%s must be a finite number", idx, key))
			}
		}
	}
	for _, key := range []string{"opacity", "generationProgress"} {
		if raw, present := elem[key]; present && raw != nil {
			value, err := coerceNumber(raw)
			if err != nil || value < 0 || value > 100 {
				return newSchemaInvalid(fmt.Sprintf("elements[%d].%s must be between 0 and 100", idx, key))
			}
		}
	}
	for _, key := range []string{"visible", "locked", "clipContent", "isGenerating", "isRecoveredTask"} {
		if raw, present := elem[key]; present && raw != nil {
			if _, ok := raw.(bool); !ok {
				return newSchemaInvalid(fmt.Sprintf("elements[%d].%s must be a boolean", idx, key))
			}
		}
	}
	for _, key := range []string{"content", "prompt", "negativePrompt", "model", "stylePreset", "aspectRatio", "generationResolution", "videoResolution", "videoModel", "videoMode", "videoMotionInput", "videoStatus", "fontFamily", "fontWeight", "fontStyle", "textDecoration", "textAlign", "color", "backgroundColor", "fill", "shapeType", "groupId", "groupName", "frameId", "sourceElementId", "parentElementId", "compositeGroupId", "taskId", "taskStatus", "taskErrorCode", "taskErrorMessage", "aiOperation", "shotLinkId"} {
		if err := validateOptionalElementString(elem, key, maxCanvasElementStringLength, fmt.Sprintf("elements[%d].%s", idx, key)); err != nil {
			return err
		}
	}
	if role, present := elem["layerRole"]; present && role != nil {
		value, ok := role.(string)
		if !ok {
			return newSchemaInvalid(fmt.Sprintf("elements[%d].layerRole must be a string", idx))
		}
		if _, ok := allowedLayerRoles[value]; !ok {
			return newSchemaInvalid(fmt.Sprintf("elements[%d].layerRole %q is not supported", idx, value))
		}
	}
	if err := validateOptionalElementString(elem, "path", maxCanvasElementPathLength, fmt.Sprintf("elements[%d].path", idx)); err != nil {
		return err
	}
	for _, key := range []string{"src", "referenceImageUrl", "videoSrc", "startFrameSrc", "endFrameSrc"} {
		if err := validateOptionalElementURL(elem, key, fmt.Sprintf("elements[%d].%s", idx, key)); err != nil {
			return err
		}
	}
	if ab, present := elem["assetBindings"]; present && ab != nil {
		binding, ok := asStringMap(ab)
		if !ok {
			return newSchemaInvalid(fmt.Sprintf("elements[%d].assetBindings must be an object", idx))
		}
		if err := validateAssetBinding(binding, fmt.Sprintf("elements[%d].assetBindings", idx)); err != nil {
			return err
		}
	}
	if mask, present := elem["mask"]; present && mask != nil {
		maskMap, ok := asStringMap(mask)
		if !ok {
			return newSchemaInvalid(fmt.Sprintf("elements[%d].mask must be an object", idx))
		}
		if err := validateElementMask(maskMap, fmt.Sprintf("elements[%d].mask", idx)); err != nil {
			return err
		}
	}
	return nil
}

func validateCanvasElementIdentifier(value, path string) error {
	if value == "" || len([]rune(value)) > maxCanvasElementIDLength || hasControlChars(value) {
		return newSchemaInvalid(path + " is invalid")
	}
	return nil
}

func validateOptionalElementString(elem map[string]interface{}, key string, maxLen int, path string) error {
	raw, present := elem[key]
	if !present || raw == nil {
		return nil
	}
	value, ok := raw.(string)
	if !ok {
		return newSchemaInvalid(path + " must be a string")
	}
	if len([]rune(value)) > maxLen || hasControlChars(value) {
		return newSchemaInvalid(path + " is invalid")
	}
	return nil
}

func validateOptionalElementURL(elem map[string]interface{}, key string, path string) error {
	raw, present := elem[key]
	if !present || raw == nil {
		return nil
	}
	value, ok := raw.(string)
	if !ok {
		return newSchemaInvalid(path + " must be a string")
	}
	if value == "" {
		return nil
	}
	if _, err := NormalizeCanvasReferenceURL(value, false); err != nil {
		return newSchemaInvalid(path + " is invalid")
	}
	return nil
}

func validateElementMask(mask map[string]interface{}, path string) error {
	urlValue, ok := mask["url"].(string)
	if !ok {
		return newSchemaInvalid(path + ".url must be a string")
	}
	if _, err := NormalizeCanvasReferenceURL(urlValue, true); err != nil {
		return newSchemaInvalid(path + ".url is invalid")
	}
	if raw, present := mask["featherPx"]; present && raw != nil {
		value, err := coerceNumber(raw)
		if err != nil || value < 0 || value > maxCanvasElementDimension {
			return newSchemaInvalid(path + ".featherPx must be a finite non-negative number")
		}
	}
	if raw, present := mask["expandPx"]; present && raw != nil {
		value, err := coerceNumber(raw)
		if err != nil || value < -12 || value > 12 {
			return newSchemaInvalid(path + ".expandPx must be a finite number between -12 and 12")
		}
	}
	if raw, present := mask["invert"]; present && raw != nil {
		if _, ok := raw.(bool); !ok {
			return newSchemaInvalid(path + ".invert must be a boolean")
		}
	}
	if err := validateOptionalElementString(mask, "sourceElementId", maxCanvasElementStringLength, path+".sourceElementId"); err != nil {
		return err
	}
	return nil
}

func asStringMap(raw interface{}) (map[string]interface{}, bool) {
	switch v := raw.(type) {
	case map[string]interface{}:
		return v, true
	case model.JSONMap:
		return map[string]interface{}(v), true
	default:
		return nil, false
	}
}

func validateShot(shot map[string]interface{}, idx int) error {
	path := fmt.Sprintf("shots[%d]", idx)
	if _, ok := shot["id"].(string); !ok {
		return newSchemaInvalid(path + ".id must be a string")
	}
	if _, ok := shot["localCardId"].(string); !ok {
		return newSchemaInvalid(path + ".localCardId must be a string")
	}
	if _, err := readNumber(shot, "shotId"); err != nil {
		return newSchemaInvalid(path + ".shotId must be a number")
	}
	if _, err := readNumber(shot, "orderIndex"); err != nil {
		return newSchemaInvalid(path + ".orderIndex must be a number")
	}
	if typeName, ok := shot["typeName"].(string); ok && typeName != string(WorkMaxTypeShot) {
		return newSchemaInvalid(fmt.Sprintf("%s.typeName must be %q, got %q", path, WorkMaxTypeShot, typeName))
	}
	return nil
}

func validateTimeline(tl map[string]interface{}) error {
	if _, err := readNumber(tl, "fps"); err != nil {
		return newSchemaInvalid("timeline.fps must be a number")
	}
	if _, err := readNumber(tl, "totalDuration"); err != nil {
		return newSchemaInvalid("timeline.totalDuration must be a number")
	}
	tracks, err := requireArray(tl, "tracks")
	if err != nil {
		return err
	}
	for i, raw := range tracks {
		track, ok := raw.(map[string]interface{})
		if !ok {
			return newSchemaInvalid(fmt.Sprintf("timeline.tracks[%d] must be an object", i))
		}
		kind, ok := track["kind"].(string)
		if !ok {
			return newSchemaInvalid(fmt.Sprintf("timeline.tracks[%d].kind must be a string", i))
		}
		if _, ok := allowedTrackKinds[kind]; !ok {
			return newSchemaInvalid(fmt.Sprintf("timeline.tracks[%d].kind %q is not supported", i, kind))
		}
		if _, ok := track["id"].(string); !ok {
			return newSchemaInvalid(fmt.Sprintf("timeline.tracks[%d].id must be a string", i))
		}
		if _, err := requireArray(track, "clips"); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflow(workflow map[string]interface{}) error {
	if rawVersion, present := workflow["graphVersion"]; present && rawVersion != nil {
		version, err := coerceNumber(rawVersion)
		if err != nil || version < 1 || math.Trunc(version) != version {
			return newSchemaInvalid("workflow.graphVersion must be a positive integer")
		}
	}
	if rawEditedAt, present := workflow["lastEditedAt"]; present && rawEditedAt != nil {
		if _, err := coerceNumber(rawEditedAt); err != nil {
			return newSchemaInvalid("workflow.lastEditedAt must be a finite number")
		}
	}
	if rawEditedBy, present := workflow["lastEditedBy"]; present && rawEditedBy != nil {
		editedBy, ok := rawEditedBy.(string)
		if !ok {
			return newSchemaInvalid("workflow.lastEditedBy must be a string")
		}
		if _, ok := allowedWorkflowEditedBy[editedBy]; !ok {
			return newSchemaInvalid(fmt.Sprintf("workflow.lastEditedBy %q is not supported", editedBy))
		}
	}

	nodes, err := requireWorkflowArray(workflow, "nodes")
	if err != nil {
		return err
	}
	if len(nodes) > maxCanvasWorkflowNodes {
		return newSchemaInvalid(fmt.Sprintf("workflow.nodes exceeds %d items", maxCanvasWorkflowNodes))
	}
	nodeIDs := make(map[string]struct{}, len(nodes))
	for i, raw := range nodes {
		node, ok := asStringMap(raw)
		if !ok {
			return newSchemaInvalid(fmt.Sprintf("workflow.nodes[%d] must be an object", i))
		}
		if err := validateWorkflowNode(node, i); err != nil {
			return err
		}
		id := node["id"].(string)
		if _, exists := nodeIDs[id]; exists {
			return newSchemaInvalid(fmt.Sprintf("workflow.nodes[%d].id is duplicated", i))
		}
		nodeIDs[id] = struct{}{}
	}

	edges, err := requireWorkflowArray(workflow, "edges")
	if err != nil {
		return err
	}
	if len(edges) > maxCanvasWorkflowEdges {
		return newSchemaInvalid(fmt.Sprintf("workflow.edges exceeds %d items", maxCanvasWorkflowEdges))
	}
	edgeIDs := make(map[string]struct{}, len(edges))
	for i, raw := range edges {
		edge, ok := asStringMap(raw)
		if !ok {
			return newSchemaInvalid(fmt.Sprintf("workflow.edges[%d] must be an object", i))
		}
		if err := validateWorkflowEdge(edge, i, nodeIDs, edgeIDs); err != nil {
			return err
		}
	}

	return nil
}

func validateWorkflowRuns(runs []interface{}) error {
	if len(runs) > maxCanvasWorkflowRuns {
		return newSchemaInvalid(fmt.Sprintf("workflowRuns exceeds %d items", maxCanvasWorkflowRuns))
	}
	for i, raw := range runs {
		run, ok := asStringMap(raw)
		if !ok {
			return newSchemaInvalid(fmt.Sprintf("workflowRuns[%d] must be an object", i))
		}
		path := fmt.Sprintf("workflowRuns[%d]", i)
		id, ok := run["id"].(string)
		if !ok {
			return newSchemaInvalid(path + ".id must be a string")
		}
		if err := validateCanvasElementIdentifier(id, path+".id"); err != nil {
			return err
		}
		status, ok := run["status"].(string)
		if !ok {
			return newSchemaInvalid(path + ".status must be a string")
		}
		if _, ok := allowedWorkflowRunStatuses[status]; !ok {
			return newSchemaInvalid(fmt.Sprintf("%s.status %q is not supported", path, status))
		}
		if status == string(WorkflowRunQueued) || status == string(WorkflowRunRunning) {
			return newSchemaInvalid(fmt.Sprintf("%s.status %q is not persistable", path, status))
		}
		if _, err := readNumber(run, "startedAt"); err != nil {
			return newSchemaInvalid(path + ".startedAt must be a finite number")
		}
		if rawCompleted, present := run["completedAt"]; present && rawCompleted != nil {
			if _, err := coerceNumber(rawCompleted); err != nil {
				return newSchemaInvalid(path + ".completedAt must be a finite number")
			}
		}
		if err := validateOptionalElementString(run, "workflowId", maxCanvasElementIDLength, path+".workflowId"); err != nil {
			return err
		}
		if rawResumeCount, present := run["resumeCount"]; present && rawResumeCount != nil {
			resumeCount, err := coerceNumber(rawResumeCount)
			if err != nil || resumeCount < 0 || math.Trunc(resumeCount) != resumeCount {
				return newSchemaInvalid(path + ".resumeCount must be a non-negative integer")
			}
		}
		if err := validateOptionalElementString(run, "lastResumedNodeId", maxCanvasElementIDLength, path+".lastResumedNodeId"); err != nil {
			return err
		}
		if rawLastResumedAt, present := run["lastResumedAt"]; present && rawLastResumedAt != nil {
			if _, err := coerceNumber(rawLastResumedAt); err != nil {
				return newSchemaInvalid(path + ".lastResumedAt must be a finite number")
			}
		}
		nodeResults, ok := normalizeInterfaceSlice(run["nodeResults"])
		if !ok {
			return newSchemaInvalid(path + ".nodeResults must be an array")
		}
		if len(nodeResults) > maxCanvasWorkflowNodes {
			return newSchemaInvalid(fmt.Sprintf("%s.nodeResults exceeds %d items", path, maxCanvasWorkflowNodes))
		}
		for j, rawResult := range nodeResults {
			result, ok := asStringMap(rawResult)
			if !ok {
				return newSchemaInvalid(fmt.Sprintf("%s.nodeResults[%d] must be an object", path, j))
			}
			if err := validateWorkflowNodeResult(result, fmt.Sprintf("%s.nodeResults[%d]", path, j)); err != nil {
				return err
			}
		}
		summary, ok := asStringMap(run["summary"])
		if !ok {
			return newSchemaInvalid(path + ".summary must be an object")
		}
		for _, key := range []string{"generatedImages", "imageOps", "videoGenerations", "skipped", "failed"} {
			value, err := readNumber(summary, key)
			if err != nil || value < 0 || math.Trunc(value) != value {
				return newSchemaInvalid(fmt.Sprintf("%s.summary.%s must be a non-negative integer", path, key))
			}
		}
	}
	return nil
}

func validateWorkflowNodeResult(result map[string]interface{}, path string) error {
	nodeID, ok := result["nodeId"].(string)
	if !ok {
		return newSchemaInvalid(path + ".nodeId must be a string")
	}
	if err := validateCanvasElementIdentifier(nodeID, path+".nodeId"); err != nil {
		return err
	}
	status, ok := result["status"].(string)
	if !ok {
		return newSchemaInvalid(path + ".status must be a string")
	}
	if _, ok := allowedWorkflowNodeRunStatuses[status]; !ok {
		return newSchemaInvalid(fmt.Sprintf("%s.status %q is not supported", path, status))
	}
	if raw, present := result["inputElementIds"]; present && raw != nil {
		inputElementIDs, ok := normalizeInterfaceSlice(raw)
		if !ok {
			return newSchemaInvalid(path + ".inputElementIds must be an array")
		}
		if len(inputElementIDs) > maxCanvasWorkflowNodes {
			return newSchemaInvalid(fmt.Sprintf("%s.inputElementIds exceeds %d items", path, maxCanvasWorkflowNodes))
		}
		for i, item := range inputElementIDs {
			id, ok := item.(string)
			if !ok {
				return newSchemaInvalid(fmt.Sprintf("%s.inputElementIds[%d] must be a string", path, i))
			}
			if err := validateCanvasElementIdentifier(id, fmt.Sprintf("%s.inputElementIds[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"outputElementId", "taskId", "workflowRunId", "workflowNodeId"} {
		if err := validateOptionalElementString(result, key, maxCanvasElementIDLength, path+"."+key); err != nil {
			return err
		}
	}
	for _, key := range []string{"errorCode", "errorMessage"} {
		if err := validateOptionalElementString(result, key, maxCanvasElementStringLength, path+"."+key); err != nil {
			return err
		}
	}
	for _, key := range []string{"startedAt", "completedAt"} {
		if raw, present := result[key]; present && raw != nil {
			if _, err := coerceNumber(raw); err != nil {
				return newSchemaInvalid(fmt.Sprintf("%s.%s must be a finite number", path, key))
			}
		}
	}
	return nil
}

func validateWorkflowNode(node map[string]interface{}, idx int) error {
	path := fmt.Sprintf("workflow.nodes[%d]", idx)
	id, ok := node["id"].(string)
	if !ok {
		return newSchemaInvalid(path + ".id must be a string")
	}
	if err := validateCanvasElementIdentifier(id, path+".id"); err != nil {
		return err
	}

	kind, ok := node["kind"].(string)
	if !ok {
		return newSchemaInvalid(path + ".kind must be a string")
	}
	if _, ok := allowedWorkflowNodeKinds[kind]; !ok {
		return newSchemaInvalid(fmt.Sprintf("%s.kind %q is not supported", path, kind))
	}

	if err := validateOptionalElementString(node, "elementId", maxCanvasElementIDLength, path+".elementId"); err != nil {
		return err
	}

	if raw, present := node["operation"]; present && raw != nil {
		operation, ok := raw.(string)
		if !ok {
			return newSchemaInvalid(path + ".operation must be a string")
		}
		if _, ok := allowedWorkflowOperations[operation]; !ok {
			return newSchemaInvalid(fmt.Sprintf("%s.operation %q is not supported", path, operation))
		}
	}

	position, ok := asStringMap(node["position"])
	if !ok {
		return newSchemaInvalid(path + ".position must be an object")
	}
	for _, key := range []string{"x", "y"} {
		value, err := readNumber(position, key)
		if err != nil || math.Abs(value) > maxCanvasElementCoordinate {
			return newSchemaInvalid(fmt.Sprintf("%s.position.%s must be a finite number", path, key))
		}
	}

	if raw, present := node["params"]; present && raw != nil {
		params, ok := asStringMap(raw)
		if !ok {
			return newSchemaInvalid(path + ".params must be an object")
		}
		if len(params) > maxCanvasWorkflowParamsKeys {
			return newSchemaInvalid(fmt.Sprintf("%s.params exceeds %d keys", path, maxCanvasWorkflowParamsKeys))
		}
		if err := validateWorkflowParamValue(params, path+".params", 0); err != nil {
			return err
		}
	}

	return nil
}

func validateWorkflowEdge(edge map[string]interface{}, idx int, nodeIDs map[string]struct{}, edgeIDs map[string]struct{}) error {
	path := fmt.Sprintf("workflow.edges[%d]", idx)
	for _, key := range []string{"id", "fromNodeId", "toNodeId"} {
		value, ok := edge[key].(string)
		if !ok {
			return newSchemaInvalid(fmt.Sprintf("%s.%s must be a string", path, key))
		}
		if err := validateCanvasElementIdentifier(value, fmt.Sprintf("%s.%s", path, key)); err != nil {
			return err
		}
	}
	edgeID := edge["id"].(string)
	if _, exists := edgeIDs[edgeID]; exists {
		return newSchemaInvalid(path + ".id is duplicated")
	}
	edgeIDs[edgeID] = struct{}{}
	for _, key := range []string{"fromNodeId", "toNodeId"} {
		value := edge[key].(string)
		if _, ok := nodeIDs[value]; !ok {
			return newSchemaInvalid(fmt.Sprintf("%s.%s references missing workflow node %q", path, key, value))
		}
	}
	for _, key := range []string{"fromPort", "toPort"} {
		if err := validateOptionalElementString(edge, key, maxCanvasElementIDLength, fmt.Sprintf("%s.%s", path, key)); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflowParamValue(value interface{}, path string, depth int) error {
	if depth > maxCanvasWorkflowParamDepth {
		return newSchemaInvalid(path + " is too deeply nested")
	}
	switch v := value.(type) {
	case nil, bool, string:
		if s, ok := v.(string); ok && (len([]rune(s)) > maxCanvasElementStringLength || hasControlChars(s)) {
			return newSchemaInvalid(path + " is invalid")
		}
		return nil
	case float64, float32, int, int32, int64:
		if _, err := coerceNumber(v); err != nil {
			return newSchemaInvalid(path + " must be finite")
		}
		return nil
	case []interface{}:
		if len(v) > maxCanvasWorkflowParamsKeys {
			return newSchemaInvalid(fmt.Sprintf("%s exceeds %d items", path, maxCanvasWorkflowParamsKeys))
		}
		for i, item := range v {
			if err := validateWorkflowParamValue(item, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
		return nil
	case map[string]interface{}:
		if len(v) > maxCanvasWorkflowParamsKeys {
			return newSchemaInvalid(fmt.Sprintf("%s exceeds %d keys", path, maxCanvasWorkflowParamsKeys))
		}
		for key, item := range v {
			if key == "" || len([]rune(key)) > maxCanvasElementIDLength || hasControlChars(key) {
				return newSchemaInvalid(path + " has invalid key")
			}
			if err := validateWorkflowParamValue(item, path+"."+key, depth+1); err != nil {
				return err
			}
		}
		return nil
	case model.JSONMap:
		return validateWorkflowParamValue(map[string]interface{}(v), path, depth)
	default:
		return newSchemaInvalid(path + " has unsupported value")
	}
}

func validateAssetBinding(binding map[string]interface{}, path string) error {
	scope, ok := binding["scope"].(string)
	if !ok {
		return newSchemaInvalid(path + ".scope must be a string")
	}
	if _, ok := allowedAssetScopes[scope]; !ok {
		return newSchemaInvalid(fmt.Sprintf("%s.scope %q is not supported", path, scope))
	}
	for _, key := range []string{"characterIds", "brandIds", "productIds"} {
		raw, present := binding[key]
		if !present || raw == nil {
			continue
		}
		list, ok := raw.([]interface{})
		if !ok {
			return newSchemaInvalid(fmt.Sprintf("%s.%s must be an array", path, key))
		}
		for i, id := range list {
			if _, err := coerceNumber(id); err != nil {
				return newSchemaInvalid(fmt.Sprintf("%s.%s[%d] must be a number", path, key, i))
			}
		}
	}
	return nil
}

func requireWorkflowArray(doc map[string]interface{}, key string) ([]interface{}, error) {
	raw, ok := doc[key]
	if !ok {
		return nil, newSchemaInvalid(fmt.Sprintf("workflow.%s is required", key))
	}
	list, ok := normalizeInterfaceSlice(raw)
	if !ok {
		return nil, newSchemaInvalid(fmt.Sprintf("workflow.%s must be an array", key))
	}
	return list, nil
}

func requireArray(doc map[string]interface{}, key string) ([]interface{}, error) {
	raw, ok := doc[key]
	if !ok {
		return nil, newSchemaInvalid(fmt.Sprintf("%s is required", key))
	}
	list, ok := normalizeInterfaceSlice(raw)
	if !ok {
		return nil, newSchemaInvalid(fmt.Sprintf("%s must be an array", key))
	}
	return list, nil
}

func normalizeInterfaceSlice(raw interface{}) ([]interface{}, bool) {
	switch v := raw.(type) {
	case []interface{}:
		return v, true
	case []map[string]interface{}:
		out := make([]interface{}, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out, true
	default:
		return nil, false
	}
}

func requireSchemaVersion(doc map[string]interface{}, expected int) error {
	raw, ok := doc["schemaVersion"]
	if !ok {
		return newSchemaInvalid("schemaVersion is required")
	}
	v, err := coerceNumber(raw)
	if err != nil {
		return newSchemaInvalid("schemaVersion must be a number")
	}
	if int(v) != expected {
		return newSchemaInvalid(fmt.Sprintf("schemaVersion must be %d, got %d", expected, int(v)))
	}
	return nil
}

func readNumber(doc map[string]interface{}, key string) (float64, error) {
	raw, ok := doc[key]
	if !ok {
		return 0, fmt.Errorf("%s missing", key)
	}
	return coerceNumber(raw)
}

func coerceNumber(raw interface{}) (float64, error) {
	switch v := raw.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, fmt.Errorf("not finite")
		}
		return v, nil
	case float32:
		out := float64(v)
		if math.IsNaN(out) || math.IsInf(out, 0) {
			return 0, fmt.Errorf("not finite")
		}
		return out, nil
	case int:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	default:
		return 0, fmt.Errorf("not a number: %T", raw)
	}
}

func newSchemaInvalid(msg string) error {
	return &SchemaError{Code: ErrCodeSchemaInvalid, Message: msg}
}
