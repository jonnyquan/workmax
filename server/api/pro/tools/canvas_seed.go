package tools

import "server/model"

// applyOptionalCanvasSeed writes the caller's seed into requestData when
// they supplied a non-negative value. Zero is a *valid* seed the user
// may have deliberately picked for a reproducible batch — we mirror the
// frontend's `seed=0` retry-preservation semantic and explicitly accept
// it. The video pipeline uses a stricter `> 0` rule (where 0 means
// "provider-chosen"); the canvas pipeline diverges on purpose.
//
// Extracted so the batch and single-shot img2img handlers share one
// tested implementation, and so future callers get the same semantic
// without re-deriving the pointer/negative guards.
func applyOptionalCanvasSeed(requestData model.JSONMap, seed *int64) {
	if seed == nil {
		return
	}
	if *seed < 0 {
		return
	}
	requestData["seed"] = *seed
}
