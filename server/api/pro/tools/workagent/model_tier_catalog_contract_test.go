package workagent

import (
	"os"
	"regexp"
	"testing"
)

// GET /api/desktop/models hands the user a model NAME to pick. That name is
// only useful if POST /api/work-agent/chat/agent accepts it as
// metadata.modelTier — otherwise the catalog advertises a choice that fails at
// send time, and the failure surfaces to the user as a broken product rather
// than as a config mistake.
//
// The catalog rows are seeded by
// migrations/20260814_add_global_model_required_tier.sql. This test reads that
// migration and asserts every model_id it seeds is in allowedModelTiers, which
// lives in this package (conversation_api.go) and is the chat endpoint's
// authority. Cross-checking against the migration rather than against a Go
// constant is deliberate: the migration is what actually populates production.
func TestSeededConversationModelsAreAcceptedByChatEndpoint(t *testing.T) {
	const migrationPath = "../../../../migrations/20260814_add_global_model_required_tier.sql"

	source, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read conversation-model seed migration: %v", err)
	}

	// Rows are seeded as ('<model_id>', 'text', ...).
	pattern := regexp.MustCompile(`\('([a-z0-9\-]+)',\s*'text'`)
	matches := pattern.FindAllStringSubmatch(string(source), -1)
	if len(matches) == 0 {
		t.Fatalf("no conversation-model rows found in %s — did the seed shape change?", migrationPath)
	}

	seeded := make(map[string]bool, len(matches))
	for _, match := range matches {
		modelID := match[1]
		seeded[modelID] = true
		if _, ok := allowedModelTiers[modelID]; !ok {
			t.Errorf("catalog seeds %q but the chat endpoint would reject it (not in allowedModelTiers)", modelID)
		}
		if normalizeModelTier(modelID) != modelID {
			t.Errorf("normalizeModelTier(%q) = %q — the catalog name must survive normalization",
				modelID, normalizeModelTier(modelID))
		}
	}

	// The reverse direction is a warning, not an error: a tier the chat
	// endpoint accepts but the catalog does not offer is invisible to Desktop
	// users. Fail loudly — silently unlisted paid tiers are how a product
	// stops selling something.
	for tier := range allowedModelTiers {
		if !seeded[tier] {
			t.Errorf("chat endpoint accepts %q but the Desktop catalog never offers it", tier)
		}
	}
}
