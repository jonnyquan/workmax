package tools

// canvas_ai_error.go · error-code constants + respondCanvasAIError
// envelope writer extracted from canvas_generation_api.go on 2026-05-15
// to complete the A-04 split. The matching test file —
// canvas_ai_error_test.go + canvas_ai_error_more_test.go — has been
// pinning this contract since the initial M2 collapse and explicitly
// expects a sibling source file with the same prefix.
//
// Distinct from the two other respondCanvas* helpers:
//
//   - respondCanvasError       (in canvas_responses.go) — legacy 200 +
//     response.ERROR envelope used by canvas-project CRUD.
//   - respondCanvasChatHTTPError (in canvas_responses.go) — chat / agent
//     surface with a reload flag for the FE classifier.
//
// respondCanvasAIError is the editing-pipeline shape: HTTP status
// carries the failure class directly, no reload semantics, and the FE
// classifier reads data.errorCode (canvasAIError*) + data.message.

import (
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	canvasAIErrorUnauthorized  = "UNAUTHORIZED"
	canvasAIErrorInvalidReq    = "INVALID_REQUEST"
	canvasAIErrorInvalidParams = "INVALID_PARAMS"
	canvasAIErrorInvalidDir    = "INVALID_DIRECTION"
	canvasAIErrorInvalidExpand = "INVALID_EXPAND_RATIO"
	canvasAIErrorMaskRequired  = "MASK_REQUIRED"
	canvasAIErrorTextRequired  = "TEXT_FIELDS_REQUIRED"
	canvasAIErrorProRequired   = "PRO_MODEL_REQUIRED"
)

func respondCanvasAIError(c *gin.Context, status int, message string, errorCode string) {
	payload := gin.H{
		"success": false,
		"message": message,
	}
	if strings.TrimSpace(errorCode) != "" {
		payload["errorCode"] = errorCode
	}
	c.JSON(status, gin.H{
		"code":    status,
		"message": message,
		"data":    payload,
	})
}
