package canvas

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxCanvasStorageKeyBytes = 64
	MaxShotSyncItems         = 500

	maxShotOrderIndex      = 100000
	maxShotTimelineValueMs = int64(7 * 24 * 60 * 60 * 1000)
)

// NormalizeCanvasStorageKey validates short user-controlled keys that are
// persisted in varchar(64) columns and later used for restore/lock matching.
func NormalizeCanvasStorageKey(raw string, field string, required bool) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		if required {
			return "", fmt.Errorf("%s is required", field)
		}
		return "", nil
	}
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("%s must be valid UTF-8", field)
	}
	if len(value) > MaxCanvasStorageKeyBytes {
		return "", fmt.Errorf("%s must be at most %d bytes", field, MaxCanvasStorageKeyBytes)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return "", fmt.Errorf("%s contains invalid characters", field)
		}
	}
	return value, nil
}

func ValidateShotLinksForSync(shots []ShotLink) error {
	if len(shots) > MaxShotSyncItems {
		return fmt.Errorf("shots must contain at most %d items", MaxShotSyncItems)
	}
	for i, shot := range shots {
		if _, err := NormalizeCanvasStorageKey(shot.LocalCardID, fmt.Sprintf("shots[%d].localCardId", i), true); err != nil {
			return err
		}
		if shot.OrderIndex < 0 || shot.OrderIndex > maxShotOrderIndex {
			return fmt.Errorf("shots[%d].orderIndex is out of range", i)
		}
		if shot.TimelineStart != nil && (*shot.TimelineStart < 0 || *shot.TimelineStart > maxShotTimelineValueMs) {
			return fmt.Errorf("shots[%d].timelineStart is out of range", i)
		}
		if shot.TimelineDuration != nil && (*shot.TimelineDuration < 0 || *shot.TimelineDuration > maxShotTimelineValueMs) {
			return fmt.Errorf("shots[%d].timelineDuration is out of range", i)
		}
	}
	return nil
}
