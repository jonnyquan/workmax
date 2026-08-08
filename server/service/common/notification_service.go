package common

import (
	"errors"
	"server/globals"
	"server/model"
	"time"

	"gorm.io/gorm"
)

type NotificationService struct{}

const defaultNotificationLimit = 50

// ListByUser returns the most recent notifications for a user, newest first.
// Soft-deleted rows (deleted_at NOT NULL) are excluded.
func (NotificationService) ListByUser(uid uint, limit int) ([]model.Notification, error) {
	if limit <= 0 || limit > 200 {
		limit = defaultNotificationLimit
	}
	var list []model.Notification
	err := globals.GraDBs["system"].
		Where("uid = ? AND deleted_at IS NULL", uid).
		Order("created_at DESC").
		Limit(limit).
		Find(&list).Error
	if err != nil {
		return nil, err
	}
	return list, nil
}

// UnreadCountByUser counts how many unread notifications a user has.
func (NotificationService) UnreadCountByUser(uid uint) (int64, error) {
	var count int64
	err := globals.GraDBs["system"].
		Model(&model.Notification{}).
		Where("uid = ? AND readed = ? AND deleted_at IS NULL", uid, false).
		Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetDetail loads a single notification. It does NOT mark the row as read;
// callers that want that behaviour should call MarkRead.
func (NotificationService) GetDetail(uid uint, id uint) (*model.Notification, error) {
	var n model.Notification
	err := globals.GraDBs["system"].
		Where("id = ? AND uid = ? AND deleted_at IS NULL", id, uid).
		First(&n).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &n, nil
}

// MarkRead flips a single notification to read. Idempotent.
func (NotificationService) MarkRead(uid uint, id uint) error {
	now := time.Now()
	result := globals.GraDBs["system"].
		Model(&model.Notification{}).
		Where("id = ? AND uid = ? AND deleted_at IS NULL AND readed = ?", id, uid, false).
		Updates(map[string]interface{}{
			"readed":  true,
			"read_at": now,
		})
	return result.Error
}

// MarkAllRead flips every unread notification for the user to read.
func (NotificationService) MarkAllRead(uid uint) (int64, error) {
	now := time.Now()
	result := globals.GraDBs["system"].
		Model(&model.Notification{}).
		Where("uid = ? AND readed = ? AND deleted_at IS NULL", uid, false).
		Updates(map[string]interface{}{
			"readed":  true,
			"read_at": now,
		})
	return result.RowsAffected, result.Error
}

// Create persists a new notification row.
func (NotificationService) Create(n *model.Notification) error {
	return globals.GraDBs["system"].Create(n).Error
}
