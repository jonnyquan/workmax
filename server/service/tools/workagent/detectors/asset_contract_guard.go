package detectors

import (
	"context"
	"fmt"
	"regexp"
)

// AssetContractGuard catches explicit bypasses of the creative asset
// protocol: fake/generic logos, CSS/text/SVG stand-ins for brand marks,
// made-up product imagery presented as if it were a real asset, and
// missing product/character source anchors hidden behind final-sounding
// delivery language.
//
// This is intentionally a text-level detector. It does not prove that a
// real image is correct; it blocks the common failure mode where the
// artifact or generation prompt admits it is using a substitute instead
// of asking for, bundling, or clearly marking source assets.
type AssetContractGuard struct{}

func (a *AssetContractGuard) Name() string { return "asset_contract_guard" }

var assetContractGuardPatterns = map[string]*regexp.Regexp{
	"fake_or_generic_logo":   regexp.MustCompile(`(?i)\b(?:fake|generic|imaginary|made[-\s]?up|brand[-\s]?like)\s+(?:brand\s+)?(?:logo|wordmark|logomark)\b`),
	"drawn_logo_substitute":  regexp.MustCompile(`(?i)\b(?:css|text|html|svg)\s+(?:drawn\s+|generated\s+|only\s+)?(?:brand\s+)?(?:logo|wordmark|logomark)\b|\b(?:draw|recreate|fake)\s+(?:the\s+)?(?:brand\s+)?(?:logo|wordmark|logomark)\s+(?:with|using)\s+(?:css|text|html|svg)\b`),
	"fake_product_asset":     regexp.MustCompile(`(?i)\b(?:fake|generic|imaginary|made[-\s]?up)\s+(?:product|sku|packaging)\s+(?:photo|image|render|mockup|asset)\b|\b(?:product|sku|packaging)\s+(?:photo|image|render|mockup|asset)\s+(?:is|will be)\s+(?:fake|generic|imaginary|made[-\s]?up)\b`),
	"missing_product_source": regexp.MustCompile(`(?i)\b(?:no|missing|without)\s+(?:real\s+|source\s+|reference\s+)?(?:product|sku|packaging)?\s*(?:photo|image|reference|source\s+asset)\b.*\b(?:final|official|realistic|production[-\s]?ready|ready\s+to\s+publish)\b|\b(?:final|official|realistic|production[-\s]?ready|ready\s+to\s+publish)\b.*\b(?:no|missing|without)\s+(?:real\s+|source\s+|reference\s+)?(?:product|sku|packaging)?\s*(?:photo|image|reference|source\s+asset)\b`),
}

var (
	characterConsistencyClaimPattern = regexp.MustCompile(`(?i)\b(?:same|consistent|recurring|returning)\s+(?:character|person|mascot|protagonist)\b|\bcharacter\s+(?:consistency|identity|continuity)\b`)
	characterAnchorEvidencePattern   = regexp.MustCompile(`(?i)(?:@character/|<character-context>|character-spec\.md|reference_image\s*:|identity[_\s-]?anchors?|canonical\s+(?:character\s+)?(?:reference|anchor))`)
	productPlaceholderCaveatPattern  = regexp.MustCompile(`(?i)(?:concept\s+only|not\s+(?:a\s+)?real\s+product\s+representation|待替换|待补充|placeholder|pending|ask\s+(?:the\s+)?user|\[product\s+image\s*:)`)
)

func (a *AssetContractGuard) Run(ctx context.Context, in Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Skipped(fmt.Sprintf("context cancelled before asset contract scan: %v", err)), nil
	}
	text := in.Artifact.Text
	if text == "" {
		return Skipped("no text in artifact to scan"), nil
	}

	issues := make([]string, 0, len(assetContractGuardPatterns))
	trace := map[string]any{}
	for category, pattern := range assetContractGuardPatterns {
		matches := pattern.FindAllString(text, 5)
		if len(matches) == 0 {
			continue
		}
		if category == "missing_product_source" && productPlaceholderCaveatPattern.MatchString(text) {
			continue
		}
		issues = append(issues, fmt.Sprintf("%s violates creative asset contract: e.g. %v", category, matches))
		trace[category] = matches
	}
	if characterConsistencyClaimPattern.MatchString(text) && !characterAnchorEvidencePattern.MatchString(text) {
		issues = append(issues, "character_anchor_missing violates creative asset contract: character consistency is claimed without @character, reference_image, character-spec.md, or identity anchor evidence")
		trace["character_anchor_missing"] = true
	}
	if len(issues) == 0 {
		return Pass(), nil
	}
	return Result{Status: StatusFail, Issues: issues, Trace: trace}, nil
}

func init() {
	Default().Register(&AssetContractGuard{})
}
