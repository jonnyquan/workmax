//go:build desktop

package pi_agent

import (
	"encoding/json"
	"os"
	"path/filepath"

	agentruntime "server/desktop/agentruntime"
)

// placeholderAPIKey mirrors local_agent's: pi treats a model without an
// apiKey as unauthenticated and hides it, even when the endpoint (Ollama,
// vLLM) ignores credentials entirely (docs/models.md). The value only ever
// travels to the user's own configured base URL.
const placeholderAPIKey = "workmax-local-no-key"

// Defaults for the models.json entry. The user's settings carry no context
// window; these are the conservative middle of what local OpenAI-compatible
// servers report, matching the docs/models.md full example.
const (
	defaultContextWindow = 128000
	defaultMaxTokens     = 32000
)

// writeModelsJSON regenerates <piHome>/models.json before each turn from the
// turn's endpoint config: one custom provider "workmax" speaking
// openai-completions at the user's base URL. PI_CODING_AGENT_DIR points pi at
// piHome, so this file — not the user's ~/.pi — is what --provider workmax
// resolves against. Cost is all zero: the user's own endpoint has no meter
// we can read.
func writeModelsJSON(piHome string, in agentruntime.TurnInput) error {
	apiKey := in.APIKey
	if apiKey == "" {
		apiKey = placeholderAPIKey
	}
	doc := map[string]any{
		"providers": map[string]any{
			"workmax": map[string]any{
				"baseUrl": in.BaseURL,
				"api":     "openai-completions",
				"apiKey":  apiKey,
				"models": []any{
					map[string]any{
						"id":            in.ModelID,
						"name":          in.ModelID,
						"input":         []string{"text"},
						"contextWindow": defaultContextWindow,
						"maxTokens":     defaultMaxTokens,
						"cost": map[string]float64{
							"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0,
						},
					},
				},
			},
		},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the file holds the user's API key.
	return os.WriteFile(filepath.Join(piHome, "models.json"), append(raw, '\n'), 0o600)
}
