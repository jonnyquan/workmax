package tools

import (
	"server/model"
	"sync"
)

type VideoPricingCatalogItem struct {
	Model            string                  `json:"model"`
	PricingStatus    VideoModelPricingStatus `json:"pricingStatus"`
	Duration         int                     `json:"duration"`
	Resolution       string                  `json:"resolution,omitempty"`
	Credits          int                     `json:"credits"`
	BillableDuration int                     `json:"billableDuration,omitempty"`
	BillableRate     string                  `json:"billableRate,omitempty"`
}

type VideoPricingCatalog struct {
	FeaturedModel      string                    `json:"featuredModel"`
	FeaturedDuration   int                       `json:"featuredDuration"`
	FeaturedResolution string                    `json:"featuredResolution,omitempty"`
	FeaturedCredits    int                       `json:"featuredCredits"`
	Items              []VideoPricingCatalogItem `json:"items"`
}

var (
	videoPricingCatalogMu    sync.RWMutex
	videoPricingCatalogCache *VideoPricingCatalog
)

var videoPricingCatalogProfiles = map[string]VideoQuoteOptions{
	model.KLING_2_6:       {Duration: 5, MotionMode: "std"},
	model.SORA_2:          {Duration: 4},
	model.VEO_31:          {Duration: 4, Resolution: "1080p"},
	model.VEO_31_FAST:     {Duration: 4, Resolution: "1080p"},
	model.SEEDANCE:        {Duration: 5, Resolution: "720p"},
	model.SEEDANCE_2:      {Duration: 5, Resolution: "720p"},
	model.SEEDANCE_2_FAST: {Duration: 5, Resolution: "720p"},
	model.MINIMAX_VIDEO:   {Duration: 6, Resolution: "768p"},
}

const featuredVideoPricingModel = model.VEO_31

func InitVideoPricingCatalogCache() {
	RefreshVideoPricingCatalogCache()
}

func RefreshVideoPricingCatalogCache() {
	catalog := buildVideoPricingCatalog()
	videoPricingCatalogMu.Lock()
	videoPricingCatalogCache = &catalog
	videoPricingCatalogMu.Unlock()
}

func GetVideoPricingCatalog() VideoPricingCatalog {
	videoPricingCatalogMu.RLock()
	cached := videoPricingCatalogCache
	videoPricingCatalogMu.RUnlock()
	if cached != nil {
		return cloneVideoPricingCatalog(*cached)
	}
	RefreshVideoPricingCatalogCache()
	videoPricingCatalogMu.RLock()
	defer videoPricingCatalogMu.RUnlock()
	if videoPricingCatalogCache == nil {
		return VideoPricingCatalog{}
	}
	return cloneVideoPricingCatalog(*videoPricingCatalogCache)
}

func buildVideoPricingCatalog() VideoPricingCatalog {
	modelIDs := VideoModelOrder()
	items := make([]VideoPricingCatalogItem, 0, len(modelIDs))
	catalog := VideoPricingCatalog{}

	for _, modelID := range modelIDs {
		opts, ok := videoPricingCatalogProfiles[modelID]
		if !ok {
			continue
		}
		quote, err := QuoteVideoCredits(modelID, opts)
		if err != nil {
			continue
		}
		item := VideoPricingCatalogItem{
			Model:            modelID,
			PricingStatus:    quote.PricingStatus,
			Duration:         quote.Duration,
			Resolution:       quote.Resolution,
			Credits:          quote.Credits,
			BillableDuration: quote.BillableDuration,
			BillableRate:     quote.BillableRate,
		}
		items = append(items, item)
		if modelID == featuredVideoPricingModel {
			catalog.FeaturedModel = item.Model
			catalog.FeaturedDuration = item.Duration
			catalog.FeaturedResolution = item.Resolution
			catalog.FeaturedCredits = item.Credits
		}
	}

	catalog.Items = items
	if catalog.FeaturedModel == "" && len(items) > 0 {
		catalog.FeaturedModel = items[0].Model
		catalog.FeaturedDuration = items[0].Duration
		catalog.FeaturedResolution = items[0].Resolution
		catalog.FeaturedCredits = items[0].Credits
	}
	return catalog
}

func cloneVideoPricingCatalog(src VideoPricingCatalog) VideoPricingCatalog {
	dst := src
	if len(src.Items) > 0 {
		dst.Items = append([]VideoPricingCatalogItem(nil), src.Items...)
	}
	return dst
}
