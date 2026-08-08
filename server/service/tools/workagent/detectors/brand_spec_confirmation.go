package detectors

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// BrandSpecConfirmation enforces the asset protocol's Step 5: if a
// thread has an explicitly unconfirmed brand-spec.md, generated output
// must carry a visible caveat/watermark instead of silently presenting
// the brand system as confirmed.
type BrandSpecConfirmation struct{}

func (b *BrandSpecConfirmation) Name() string { return "brand_spec_confirmation" }

var unconfirmedBrandSpecPattern = regexp.MustCompile(`(?i)\[(?:unconfirmed|pending confirmation)\]|状态\s*[:：]?\s*(?:未确认|待确认)|待品牌方确认`)

func (b *BrandSpecConfirmation) Run(ctx context.Context, in Input) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Skipped(fmt.Sprintf("context cancelled before brand spec confirmation scan: %v", err)), nil
	}
	if strings.TrimSpace(in.ThreadDir) == "" {
		return Skipped("no thread workdir"), nil
	}
	specPath := filepath.Join(in.ThreadDir, "brand-spec.md")
	data, err := os.ReadFile(specPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Skipped("no brand-spec.md in thread workdir"), nil
		}
		return Skipped(fmt.Sprintf("read brand-spec.md: %v", err)), nil
	}
	if !unconfirmedBrandSpecPattern.Match(data) {
		return Pass(), nil
	}
	if strings.TrimSpace(in.Artifact.Text) == "" {
		return Fail("brand-spec.md is unconfirmed, but artifact text is empty and cannot show a visible confirmation caveat"), nil
	}
	if unconfirmedBrandSpecPattern.MatchString(in.Artifact.Text) {
		return Pass(), nil
	}
	return Fail("brand-spec.md is unconfirmed; artifact must visibly mark brand system as pending confirmation"), nil
}

func init() {
	Default().Register(&BrandSpecConfirmation{})
}
