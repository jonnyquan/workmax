package workagent

import (
	"fmt"
	"time"

	"server/globals"
	"server/model"
)

// LoadOwnerNickname returns the display nickname for a user id, or
// "Anonymous User" when the lookup fails.
//
// Why this lives in the workagent service even though it queries the
// w_user table from a different module: the public share endpoints
// (conversation_share.go) need it to populate the "owner" field of
// the response, and the api package shouldn't be reaching directly
// into globals.GraDBs anymore. The cross-module table reference is
// fenced here behind one named function so a future schema refactor
// has exactly one place to update.
//
// Best-effort: failure is logged-and-fallback rather than surfaced.
// The share UI tolerates an anonymous owner string fine; failing the
// whole share fetch on a stale w_user lookup would be much worse UX.
func LoadOwnerNickname(uid uint) string {
	if uid == 0 {
		return "Anonymous User"
	}
	var row struct {
		Nickname string `gorm:"column:nickname"`
	}
	if err := globals.GraDBs["system"].
		Table("w_user").
		Select("nickname").
		Where("id = ?", uid).
		First(&row).Error; err != nil {
		globals.Warn(fmt.Sprintf("[workagent] LoadOwnerNickname failed for uid=%d: %v", uid, err))
		return "Anonymous User"
	}
	if row.Nickname == "" {
		return "Anonymous User"
	}
	return row.Nickname
}

// LoadOwnerAvatar returns the user's public avatar URL/path for share
// display. Empty or missing users return an empty string so API callers
// can serialize a JSON null without failing the share response.
func LoadOwnerAvatar(uid uint) string {
	if uid == 0 {
		return ""
	}
	var row struct {
		Avatar string `gorm:"column:avatar"`
	}
	if err := globals.GraDBs["system"].
		Table("w_user").
		Select("avatar").
		Where("id = ?", uid).
		First(&row).Error; err != nil {
		globals.Warn(fmt.Sprintf("[workagent] LoadOwnerAvatar failed for uid=%d: %v", uid, err))
		return ""
	}
	return row.Avatar
}

// IsUserPremium returns true when the user has an active premium
// membership (Member tier > 1 AND the membership window hasn't
// expired). Used by the upload-size limit path so the api package
// stops reaching for `globals.GraDBs["system"].First(&user, uid)`
// inline for a one-field check.
//
// Best-effort: a DB error or missing user is treated as "not
// premium" rather than surfaced. A false negative just means a
// premium user gets a smaller upload cap until the next call —
// preferable to failing the upload entirely on a transient hiccup.
func IsUserPremium(uid uint) bool {
	if uid == 0 {
		return false
	}
	user := &model.User{}
	if err := globals.GraDBs["system"].First(user, uid).Error; err != nil {
		return false
	}
	return user.Member > 1 && time.Now().Before(user.MemberEndTime)
}
