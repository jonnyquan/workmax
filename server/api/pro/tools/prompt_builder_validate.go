package tools

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Validation + normalization helpers for prompt-builder payloads. Handlers in
// prompt_builder_api.go call into these before touching the database or the
// LLM client. Tables referenced here (allowed blocks, field kinds, aliases,
// character array fields) live in prompt_builder_schema.go.

// parseLLMJSONContent accepts a raw LLM string response and returns the parsed
// object. Tolerates ```json fenced blocks and leading/trailing prose by
// falling back to the outermost `{...}` span.
func parseLLMJSONContent(content string) (map[string]interface{}, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, fmt.Errorf("empty AI response")
	}

	parse := func(candidate string) (map[string]interface{}, error) {
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(candidate), &parsed); err != nil {
			return nil, err
		}
		return parsed, nil
	}

	if parsed, err := parse(trimmed); err == nil {
		return parsed, nil
	}

	cleaned := strings.TrimPrefix(trimmed, "```json")
	cleaned = strings.TrimPrefix(cleaned, "```")
	cleaned = strings.TrimSuffix(cleaned, "```")
	cleaned = strings.TrimSpace(cleaned)
	if parsed, err := parse(cleaned); err == nil {
		return parsed, nil
	}

	start := strings.Index(cleaned, "{")
	end := strings.LastIndex(cleaned, "}")
	if start >= 0 && end > start {
		return parse(strings.TrimSpace(cleaned[start : end+1]))
	}

	return nil, fmt.Errorf("invalid JSON payload")
}

func isPromptBuilderMode(mode string) bool {
	_, ok := promptBuilderAllowedModes[strings.TrimSpace(mode)]
	return ok
}

func isPromptBuilderFieldAllowed(blockType, field string) bool {
	fields, ok := promptBuilderBlockFields[blockType]
	if !ok {
		return false
	}
	for _, allowed := range fields {
		if allowed == field {
			return true
		}
	}
	return false
}

// validateAndNormalizePromptBuilderBlocksJSON accepts either a raw blocks
// array or a versioned {schema_version, blocks} envelope, filters to allowed
// block types, sanitizes each block's data map, and returns the canonical
// versioned envelope as JSON text.
func validateAndNormalizePromptBuilderBlocksJSON(raw string) (string, error) {
	payload := strings.TrimSpace(raw)
	if payload == "" {
		return "", fmt.Errorf("empty payload")
	}
	if len(payload) > maxPromptBuilderBlocksJSONSize {
		return "", fmt.Errorf("payload too large")
	}

	var decoded interface{}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return "", fmt.Errorf("invalid JSON")
	}

	schemaVersion := promptBuilderSchemaVersion
	var rawBlocks []interface{}
	switch v := decoded.(type) {
	case []interface{}:
		rawBlocks = v
	case map[string]interface{}:
		if maybeBlocks, ok := v["blocks"].([]interface{}); ok {
			rawBlocks = maybeBlocks
		} else {
			return "", fmt.Errorf("invalid blocks array")
		}
		if version, ok := toPromptBuilderSchemaVersion(v["schema_version"]); ok {
			schemaVersion = version
		}
	default:
		return "", fmt.Errorf("unsupported payload type")
	}

	blocks := make([]map[string]interface{}, 0, len(rawBlocks))
	for _, rawBlock := range rawBlocks {
		blockMap, ok := rawBlock.(map[string]interface{})
		if !ok {
			continue
		}

		blockType, ok := blockMap["type"].(string)
		if !ok {
			continue
		}
		blockType = strings.TrimSpace(blockType)
		if _, exists := promptBuilderAllowedBlockTypes[blockType]; !exists {
			continue
		}

		dataMap, _ := blockMap["data"].(map[string]interface{})
		sanitizedData := sanitizePromptBuilderBlockData(blockType, dataMap)

		sanitizedBlock := map[string]interface{}{
			"type": blockType,
			"data": sanitizedData,
		}

		if idText, ok := normalizePromptBuilderText(blockMap["id"]); ok && len(idText) <= 128 {
			sanitizedBlock["id"] = idText
		}
		if collapsed, ok := blockMap["collapsed"].(bool); ok {
			sanitizedBlock["collapsed"] = collapsed
		}
		if disabled, ok := blockMap["disabled"].(bool); ok {
			sanitizedBlock["disabled"] = disabled
		}

		blocks = append(blocks, sanitizedBlock)
	}

	if len(blocks) == 0 {
		return "", fmt.Errorf("no valid blocks")
	}

	normalized := map[string]interface{}{
		"schema_version": schemaVersion,
		"blocks":         blocks,
	}
	out, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("failed to marshal normalized payload")
	}
	return string(out), nil
}

func toPromptBuilderSchemaVersion(value interface{}) (int, bool) {
	switch v := value.(type) {
	case float64:
		ver := int(v)
		if ver > 0 && ver <= 10 {
			return ver, true
		}
	case int:
		if v > 0 && v <= 10 {
			return v, true
		}
	case int64:
		ver := int(v)
		if ver > 0 && ver <= 10 {
			return ver, true
		}
	}
	return 0, false
}

// sanitizePromptBuilderParsedResult is the counterpart used by the Parse
// endpoints (text → blocks and image → blocks). It mirrors the blocks-JSON
// validator but keeps the caller-visible {mode, blocks} shape.
func sanitizePromptBuilderParsedResult(parsed map[string]interface{}) (map[string]interface{}, error) {
	if parsed == nil {
		return nil, fmt.Errorf("empty parsed payload")
	}

	mode := "freeform"
	if rawMode, ok := parsed["mode"].(string); ok && isPromptBuilderMode(rawMode) {
		mode = strings.TrimSpace(rawMode)
	}

	rawBlocks, ok := parsed["blocks"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid blocks payload")
	}

	blocks := make([]map[string]interface{}, 0, len(rawBlocks))
	for _, rawBlock := range rawBlocks {
		blockMap, ok := rawBlock.(map[string]interface{})
		if !ok {
			continue
		}

		blockType, ok := blockMap["type"].(string)
		if !ok {
			continue
		}
		blockType = strings.TrimSpace(blockType)
		if _, exists := promptBuilderAllowedBlockTypes[blockType]; !exists {
			continue
		}

		dataMap, _ := blockMap["data"].(map[string]interface{})
		sanitizedData := sanitizePromptBuilderBlockData(blockType, dataMap)
		if len(sanitizedData) == 0 {
			continue
		}

		blocks = append(blocks, map[string]interface{}{
			"type": blockType,
			"data": sanitizedData,
		})
	}

	if len(blocks) == 0 {
		return nil, fmt.Errorf("no valid blocks")
	}

	return map[string]interface{}{
		"mode":   mode,
		"blocks": blocks,
	}, nil
}

func sanitizePromptBuilderBlockData(blockType string, raw map[string]interface{}) map[string]interface{} {
	if raw == nil {
		return map[string]interface{}{}
	}

	fields := promptBuilderBlockFields[blockType]
	sanitized := make(map[string]interface{}, len(fields)+1)

	for _, field := range fields {
		value, found := pickPromptBuilderFieldValue(raw, field)
		if !found || value == nil {
			continue
		}

		kind, ok := promptBuilderFieldKinds[field]
		if !ok {
			kind = promptBuilderFieldKindString
		}

		switch kind {
		case promptBuilderFieldKindStringArray:
			arr := normalizePromptBuilderStringArray(value)
			if len(arr) > 0 {
				sanitized[field] = arr
			}
		case promptBuilderFieldKindString:
			if text, ok := normalizePromptBuilderText(value); ok {
				sanitized[field] = text
			}
		case promptBuilderFieldKindBoolean:
			if booleanValue, ok := normalizePromptBuilderBoolean(value); ok {
				if booleanValue {
					sanitized[field] = true
				}
			}
		case promptBuilderFieldKindNumber:
			numberValue, ok := normalizePromptBuilderNumber(value)
			if !ok {
				continue
			}
			switch field {
			case "realism_level", "detail_level", "intensity":
				numberValue = clampPromptBuilderNumber(numberValue, 0, 100)
				if numberValue == 50 {
					continue
				}
			case "weight":
				numberValue = clampPromptBuilderNumber(numberValue, 0.1, 2.0)
				if numberValue == 1.0 {
					continue
				}
			}
			sanitized[field] = numberValue
		case promptBuilderFieldKindObject:
			if objectValue, ok := sanitizePromptBuilderObjectField(field, value); ok {
				sanitized[field] = objectValue
			}
		case promptBuilderFieldKindObjectArray:
			if objectArray, ok := sanitizePromptBuilderObjectArrayField(field, value); ok && len(objectArray) > 0 {
				sanitized[field] = objectArray
			}
		}
	}

	// `weight` is supported for most block types even though it is not listed in each block schema.
	if value, found := pickPromptBuilderFieldValue(raw, "weight"); found && value != nil {
		if numberValue, ok := normalizePromptBuilderNumber(value); ok {
			normalizedWeight := clampPromptBuilderNumber(numberValue, 0.1, 2.0)
			if normalizedWeight != 1.0 {
				sanitized["weight"] = normalizedWeight
			}
		}
	}

	return sanitized
}

func sanitizePromptBuilderObjectField(field string, value interface{}) (map[string]interface{}, bool) {
	obj, ok := value.(map[string]interface{})
	if !ok || obj == nil {
		return nil, false
	}

	switch field {
	case "style_fusion_blend":
		out := map[string]interface{}{}
		if style1, ok := normalizePromptBuilderText(obj["style1"]); ok {
			out["style1"] = style1
		}
		if style2, ok := normalizePromptBuilderText(obj["style2"]); ok {
			out["style2"] = style2
		}
		if weight, ok := normalizePromptBuilderNumber(obj["weight"]); ok {
			out["weight"] = clampPromptBuilderNumber(weight, 0, 100)
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	default:
		return nil, false
	}
}

func sanitizePromptBuilderObjectArrayField(field string, value interface{}) ([]map[string]interface{}, bool) {
	rawArray, ok := value.([]interface{})
	if !ok || len(rawArray) == 0 {
		return nil, false
	}

	out := make([]map[string]interface{}, 0, len(rawArray))
	switch field {
	case "characters":
		for _, rawItem := range rawArray {
			itemMap, ok := rawItem.(map[string]interface{})
			if !ok || itemMap == nil {
				continue
			}
			item := map[string]interface{}{}
			if id, ok := normalizePromptBuilderText(itemMap["id"]); ok {
				item["id"] = id
			}
			if name, ok := normalizePromptBuilderText(itemMap["name"]); ok {
				item["name"] = name
			}
			if charType, ok := normalizePromptBuilderText(itemMap["type"]); ok {
				item["type"] = charType
			}
			for _, fieldName := range promptBuilderCharacterArrayFields {
				if values := normalizePromptBuilderStringArray(itemMap[fieldName]); len(values) > 0 {
					item[fieldName] = values
				}
			}
			if pose, ok := normalizePromptBuilderText(itemMap["pose"]); ok {
				item["pose"] = pose
			}
			if description, ok := normalizePromptBuilderText(itemMap["description"]); ok {
				item["description"] = description
			}
			if len(item) > 0 {
				out = append(out, item)
			}
		}
	case "uploaded_images":
		for _, rawItem := range rawArray {
			item := map[string]interface{}{}
			switch typed := rawItem.(type) {
			case map[string]interface{}:
				if id, ok := normalizePromptBuilderText(typed["id"]); ok {
					item["id"] = id
				}
				if name, ok := normalizePromptBuilderText(typed["name"]); ok {
					item["name"] = name
				}
				if url, ok := normalizePromptBuilderText(typed["url"]); ok {
					item["url"] = url
				}
				if mimeType, ok := normalizePromptBuilderText(typed["type"]); ok {
					item["type"] = mimeType
				}
				if description, ok := normalizePromptBuilderText(typed["description"]); ok {
					item["description"] = description
				}
				if size, ok := normalizePromptBuilderNumber(typed["size"]); ok {
					item["size"] = size
				}
			default:
				if url, ok := normalizePromptBuilderText(rawItem); ok {
					item["url"] = url
				}
			}
			if len(item) > 0 {
				out = append(out, item)
			}
		}
	case "invisible_verbs":
		for _, rawItem := range rawArray {
			itemMap, ok := rawItem.(map[string]interface{})
			if !ok || itemMap == nil {
				continue
			}
			item := map[string]interface{}{}
			if verb, ok := normalizePromptBuilderText(itemMap["verb"]); ok {
				item["verb"] = verb
			}
			if target, ok := normalizePromptBuilderText(itemMap["target"]); ok {
				item["target"] = target
			}
			if len(item) > 0 {
				out = append(out, item)
			}
		}
	}

	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func normalizePromptBuilderBoolean(value interface{}) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case float64:
		return v != 0, true
	case float32:
		return v != 0, true
	case int:
		return v != 0, true
	case int64:
		return v != 0, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		default:
			return false, false
		}
	default:
		return false, false
	}
}

func normalizePromptBuilderNumber(value interface{}) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func clampPromptBuilderNumber(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func pickPromptBuilderFieldValue(raw map[string]interface{}, field string) (interface{}, bool) {
	if value, ok := raw[field]; ok {
		return value, true
	}
	if aliases, ok := promptBuilderFieldAliases[field]; ok {
		for _, alias := range aliases {
			if value, exists := raw[alias]; exists {
				return value, true
			}
		}
	}
	return nil, false
}

func normalizePromptBuilderStringArray(value interface{}) []string {
	switch v := value.(type) {
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text, ok := normalizePromptBuilderText(item); ok {
				out = append(out, text)
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text, ok := normalizePromptBuilderText(item); ok {
				out = append(out, text)
			}
		}
		return out
	case string:
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if text, ok := normalizePromptBuilderText(part); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return []string{}
	}
}

func normalizePromptBuilderText(value interface{}) (string, bool) {
	switch v := value.(type) {
	case string:
		text := strings.TrimSpace(v)
		return text, text != ""
	case fmt.Stringer:
		text := strings.TrimSpace(v.String())
		return text, text != ""
	default:
		return "", false
	}
}
