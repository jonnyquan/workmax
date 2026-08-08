package initialize

import (
	"server/globals"
	canvasService "server/service/tools/canvas"
	"server/utils"

	"github.com/songzhibin97/gkit/cache/local_cache"
)

func OtherInit() {
	dr, err := utils.ParseDuration(globals.GraConf.JWT.ExpiresTime)
	if err != nil {
		panic(err)
	}
	_, err = utils.ParseDuration(globals.GraConf.JWT.BufferTime)
	if err != nil {
		panic(err)
	}

	globals.GraCache = local_cache.NewCache(
		local_cache.SetDefaultExpire(dr),
	)

	// §12.3 PromptGuard wiring. Translates the YAML knobs in
	// canvas.prompt_guard into the canvas service Config and installs
	// the process-wide singleton. Without this call the guard runs
	// with an empty SensitiveWords list (reject path inert) — only
	// length truncation + injection warnings would fire.
	pg := globals.GraConf.Canvas.PromptGuard
	canvasService.Configure(canvasService.Config{
		MaxPromptLen:         pg.MaxPromptLen,
		MaxNegativePromptLen: pg.MaxNegativePromptLen,
		SensitiveWords:       pg.SensitiveWords,
		BlockedPatterns:      pg.BlockedPatterns,
	})

	globals.Info("System initialization completed with local template registries")
}
