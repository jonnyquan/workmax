package tools

import (
	"sync"

	"server/model"
)

type ToolPricingCatalogExamples struct {
	BasicImageCredits int `json:"basicImageCredits"`
	Nano2ImageCredits int `json:"nano2ImageCredits"`
	ProImageCredits   int `json:"proImageCredits"`
	VideoCredits      int `json:"videoCredits"`
}

type ToolPricingCatalog struct {
	Examples ToolPricingCatalogExamples `json:"examples"`
}

var (
	toolPricingCatalogMu    sync.RWMutex
	toolPricingCatalogCache *ToolPricingCatalog
)

func InitToolPricingCatalogCache() {
	RefreshToolPricingCatalogCache()
}

func RefreshToolPricingCatalogCache() {
	catalog := buildToolPricingCatalog()
	toolPricingCatalogMu.Lock()
	toolPricingCatalogCache = &catalog
	toolPricingCatalogMu.Unlock()
}

func GetToolPricingCatalog() ToolPricingCatalog {
	toolPricingCatalogMu.RLock()
	cached := toolPricingCatalogCache
	toolPricingCatalogMu.RUnlock()
	if cached != nil {
		return *cached
	}
	RefreshToolPricingCatalogCache()
	toolPricingCatalogMu.RLock()
	defer toolPricingCatalogMu.RUnlock()
	if toolPricingCatalogCache == nil {
		return ToolPricingCatalog{}
	}
	return *toolPricingCatalogCache
}

func buildToolPricingCatalog() ToolPricingCatalog {
	videoCatalog := GetVideoPricingCatalog()
	return ToolPricingCatalog{
		Examples: ToolPricingCatalogExamples{
			BasicImageCredits: GetCreditCostByToolID(CreditCostParams{
				ToolID: model.TOOL_IMAGE_GENERATOR,
				Model:  model.NANO_BANANA,
			}),
			Nano2ImageCredits: GetCreditCostByToolID(CreditCostParams{
				ToolID: model.TOOL_IMAGE_GENERATOR,
				Model:  model.NANO_BANANA_2,
			}),
			ProImageCredits: GetCreditCostByToolID(CreditCostParams{
				ToolID:     model.TOOL_IMAGE_GENERATOR,
				Model:      model.NANO_BANANA_PRO,
				Resolution: "2k",
			}),
			VideoCredits: videoCatalog.FeaturedCredits,
		},
	}
}
