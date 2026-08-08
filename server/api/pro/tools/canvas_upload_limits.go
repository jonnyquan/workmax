package tools

import (
	"strings"

	toolsService "server/service/tools"
)

// canvas_upload_limits.go — upload-size policy helpers for the
// canvas asset surfaces. Lifted out of canvas_api.go to keep the
// handler file focused on HTTP plumbing and to give the policy
// helpers a single home where they can be tested without a
// http.ResponseWriter in scope.
//
// The four exported size functions form a layered policy:
//
//   - Image uploads: max(getMaxReferenceImageSize, 16MiB)
//   - Other media (video/audio/etc.): max(GetMaxRemoteAssetSize, 16MiB)
//   - Outer request body limit: max of the two above
//   - Per-content-type dispatch: image/* → image, everything else → media
//
// The 16MiB floor exists because the per-tool size caps may be
// configured below it; the canvas surface always accepts at
// least 16MiB regardless. The handlers use the body-limit at the
// http.MaxBytesReader boundary and the per-content-type size as
// the inner cap on the parsed multipart body.

func maxCanvasUploadSize(values ...int64) int64 {
	var max int64
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func getCanvasAssetImageUploadSize() int64 {
	return maxCanvasUploadSize(getMaxReferenceImageSize(), 16*1024*1024)
}

func getCanvasAssetMediaUploadSize() int64 {
	return maxCanvasUploadSize(toolsService.GetMaxRemoteAssetSize(), 16*1024*1024)
}

func getCanvasAssetUploadBodyLimit() int64 {
	return maxCanvasUploadSize(getCanvasAssetImageUploadSize(), getCanvasAssetMediaUploadSize())
}

func getCanvasAssetUploadSizeForContentType(contentType string) int64 {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "image/") {
		return getCanvasAssetImageUploadSize()
	}
	return getCanvasAssetMediaUploadSize()
}
