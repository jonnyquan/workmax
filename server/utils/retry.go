package utils

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	// "server/agent" // removed - agent system deleted
	"server/globals"
)

// RetryConfig defines the configuration for retry logic
type RetryConfig struct {
	MaxRetries    int
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	BackoffFactor float64
	RetryOnErrors []string
}

// RetryableError represents an error that can be retried
type RetryableError struct {
	ErrorType string
	Message   string
	Retryable bool
}

func (e *RetryableError) Error() string {
	return e.Message
}

// NewDefaultRetryConfig creates a default retry configuration
func NewDefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:    3,
		InitialDelay:  2 * time.Second,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 2.0,
		RetryOnErrors: []string{"timeout", "connection", "network"},
	}
}

// WithRetry executes a function with retry logic
func WithRetry[T any](ctx context.Context, config *RetryConfig, operation func() (T, error)) (T, error) {
	var result T
	var lastError error

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		result, err := operation()
		if err == nil {
			// Success - return immediately
			return result, nil
		}

		lastError = err
		retryableErr := classifyError(err)

		// Check if error is retryable
		if !retryableErr.Retryable || !shouldRetryOnError(retryableErr.ErrorType, config.RetryOnErrors) {
			globals.GraLog.Warn(fmt.Sprintf("Non-retryable error on attempt %d: %s", attempt+1, err.Error()))
			return result, err
		}

		// Don't wait after the last attempt
		if attempt == config.MaxRetries {
			globals.GraLog.Error(fmt.Sprintf("Max retries (%d) exceeded. Last error: %s", config.MaxRetries, err.Error()))
			break
		}

		// Calculate delay with exponential backoff
		delay := calculateDelay(attempt, config.InitialDelay, config.MaxDelay, config.BackoffFactor)

		globals.GraLog.Info(fmt.Sprintf("Retryable error on attempt %d, retrying in %v: %s",
			attempt+1, delay, retryableErr.Message))

		// Wait before retry
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(delay):
		}
	}

	return result, lastError
}

// WithRetryAsync executes a function with retry logic and progress updates
func WithRetryAsync[T any](ctx context.Context, config *RetryConfig, operation func() (T, error),
	onProgress func(attempt int, maxAttempts int, delay time.Duration, err error)) (T, error) {

	var result T
	var lastError error

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		result, err := operation()
		if err == nil {
			// Success
			if onProgress != nil {
				onProgress(attempt+1, config.MaxRetries+1, 0, nil)
			}
			return result, nil
		}

		lastError = err
		retryableErr := classifyError(err)

		if !retryableErr.Retryable || !shouldRetryOnError(retryableErr.ErrorType, config.RetryOnErrors) {
			if onProgress != nil {
				onProgress(attempt+1, config.MaxRetries+1, 0, err)
			}
			return result, err
		}

		if attempt == config.MaxRetries {
			if onProgress != nil {
				onProgress(attempt+1, config.MaxRetries+1, 0, err)
			}
			break
		}

		delay := calculateDelay(attempt, config.InitialDelay, config.MaxDelay, config.BackoffFactor)

		// Notify progress
		if onProgress != nil {
			onProgress(attempt+1, config.MaxRetries+1, delay, err)
		}

		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(delay):
		}
	}

	return result, lastError
}

// classifyError determines if an error is retryable and its type
func classifyError(err error) *RetryableError {
	errMsg := strings.ToLower(err.Error())

	// Network errors
	if strings.Contains(errMsg, "connection refused") ||
		strings.Contains(errMsg, "connection reset") ||
		strings.Contains(errMsg, "connection timeout") ||
		strings.Contains(errMsg, "network") {
		return &RetryableError{
			ErrorType: "network_error",
			Message:   err.Error(),
			Retryable: true,
		}
	}

	// Rate limiting
	if strings.Contains(errMsg, "rate limit") ||
		strings.Contains(errMsg, "too many requests") ||
		strings.Contains(errMsg, "quota exceeded") {
		return &RetryableError{
			ErrorType: "rate_limit_exceeded",
			Message:   err.Error(),
			Retryable: true,
		}
	}

	// Model overloaded — broadened from a single "model overloaded"
	// substring after gateway responses like "The model is overloaded.
	// Please try again later." were skipping the retry path. Any
	// occurrence of "overloaded" in an error message is overwhelmingly
	// capacity pressure, regardless of intervening words.
	if strings.Contains(errMsg, "overloaded") ||
		strings.Contains(errMsg, "service unavailable") ||
		strings.Contains(errMsg, "temporarily unavailable") {
		return &RetryableError{
			ErrorType: "model_overloaded",
			Message:   err.Error(),
			Retryable: true,
		}
	}

	// Timeout errors
	if strings.Contains(errMsg, "timeout") ||
		strings.Contains(errMsg, "deadline exceeded") {
		return &RetryableError{
			ErrorType: "timeout",
			Message:   err.Error(),
			Retryable: true,
		}
	}

	// Server errors (5xx)
	if strings.Contains(errMsg, "internal server error") ||
		strings.Contains(errMsg, "bad gateway") ||
		strings.Contains(errMsg, "service unavailable") ||
		strings.Contains(errMsg, "gateway timeout") {
		return &RetryableError{
			ErrorType: "server_error_5xx",
			Message:   err.Error(),
			Retryable: true,
		}
	}

	// Client errors (4xx) - usually not retryable
	if strings.Contains(errMsg, "bad request") ||
		strings.Contains(errMsg, "unauthorized") ||
		strings.Contains(errMsg, "forbidden") ||
		strings.Contains(errMsg, "not found") {
		return &RetryableError{
			ErrorType: "client_error_4xx",
			Message:   err.Error(),
			Retryable: false,
		}
	}

	// Default: unknown error, not retryable
	return &RetryableError{
		ErrorType: "unknown_error",
		Message:   err.Error(),
		Retryable: false,
	}
}

// shouldRetryOnError checks if the error type should trigger a retry
func shouldRetryOnError(errorType string, retryOnErrors []string) bool {
	for _, retryableType := range retryOnErrors {
		if errorType == retryableType {
			return true
		}
	}
	return false
}

// calculateDelay calculates the delay for the next retry attempt using exponential backoff
func calculateDelay(attempt int, initialDelay, maxDelay time.Duration, backoffFactor float64) time.Duration {
	delay := float64(initialDelay) * math.Pow(backoffFactor, float64(attempt))

	// Add jitter (±10% randomization)
	jitter := delay * 0.1 * (2.0*rand.Float64() - 1.0)
	delay += jitter

	// Cap at max delay
	if delay > float64(maxDelay) {
		delay = float64(maxDelay)
	}

	return time.Duration(delay)
}

// RetryStats represents statistics about retry operations
type RetryStats struct {
	TotalAttempts   int
	SuccessAttempts int
	FailedAttempts  int
	TotalDelay      time.Duration
	LastErrorType   string
}

// NewRetryStats creates a new retry statistics tracker
func NewRetryStats() *RetryStats {
	return &RetryStats{}
}

// RecordAttempt records a retry attempt
func (rs *RetryStats) RecordAttempt(success bool, delay time.Duration, errorType string) {
	rs.TotalAttempts++
	rs.TotalDelay += delay

	if success {
		rs.SuccessAttempts++
	} else {
		rs.FailedAttempts++
		rs.LastErrorType = errorType
	}
}

// SuccessRate returns the success rate as a percentage
func (rs *RetryStats) SuccessRate() float64 {
	if rs.TotalAttempts == 0 {
		return 0
	}
	return float64(rs.SuccessAttempts) / float64(rs.TotalAttempts) * 100
}

// AverageDelay returns the average delay between attempts
func (rs *RetryStats) AverageDelay() time.Duration {
	if rs.TotalAttempts == 0 {
		return 0
	}
	return rs.TotalDelay / time.Duration(rs.TotalAttempts)
}
