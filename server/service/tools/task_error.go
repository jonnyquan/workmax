package tools

import "strings"

const (
	TaskErrorCodeUnknown            = "TASK_FAILED"
	TaskErrorCodeUserCancelled      = "USER_CANCELLED"
	TaskErrorCodeModelTimeout       = "MODEL_TIMEOUT"
	TaskErrorCodeQueueBusy          = "QUEUE_BUSY"
	TaskErrorCodeRateLimited        = "RATE_LIMITED"
	TaskErrorCodeReferenceInvalid   = "REFERENCE_IMAGE_INVALID"
	TaskErrorCodeInsufficientCredit = "INSUFFICIENT_CREDITS"
	TaskErrorCodeProviderMissing    = "PROVIDER_MISSING"
	TaskErrorCodeVideoProvider      = "VIDEO_PROVIDER_MISSING"
)

// ClassifyTaskErrorCode 将任务错误消息映射为稳定错误码，便于前端细分处理。
func ClassifyTaskErrorCode(errorMsg string) string {
	msg := strings.ToLower(strings.TrimSpace(errorMsg))
	if msg == "" {
		return ""
	}

	switch {
	case strings.Contains(msg, "cancelled by user"), strings.Contains(msg, "task was cancelled"):
		return TaskErrorCodeUserCancelled
	case strings.Contains(msg, "timed out"), strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"):
		return TaskErrorCodeModelTimeout
	case strings.Contains(msg, "no available video generation provider"), strings.Contains(msg, "media_type=video"):
		return TaskErrorCodeVideoProvider
	case strings.Contains(msg, "no available image generation provider"), (strings.Contains(msg, "no available") && strings.Contains(msg, "provider")):
		return TaskErrorCodeProviderMissing
	case strings.Contains(msg, "queue is full"), strings.Contains(msg, "service is busy"), strings.Contains(msg, "task queue"):
		return TaskErrorCodeQueueBusy
	case strings.Contains(msg, "rate limit"), strings.Contains(msg, "too many requests"):
		return TaskErrorCodeRateLimited
	case strings.Contains(msg, "reference image"), strings.Contains(msg, "invalid file type"), strings.Contains(msg, "file too large"):
		return TaskErrorCodeReferenceInvalid
	case strings.Contains(msg, "insufficient credits"):
		return TaskErrorCodeInsufficientCredit
	default:
		return TaskErrorCodeUnknown
	}
}
