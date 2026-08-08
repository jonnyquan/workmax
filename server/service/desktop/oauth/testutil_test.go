package oauth

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	model "server/model/desktop/oauth"
)

// newTestDB returns an in-memory SQLite gorm.DB with the three OAuth
// tables migrated. Each test gets its own fresh DB so state doesn't
// leak between cases.
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open in-memory sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.Client{}, &model.AuthorizationCode{}, &model.RefreshToken{}); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	return db
}

// seedDesktopClient inserts the workmax-desktop public client row
// (mirrors what the SQL migration's seed does). Returns the inserted
// row for tests that want its ID.
func seedDesktopClient(t *testing.T, db *gorm.DB) *model.Client {
	t.Helper()
	c := model.Client{
		ClientID:      model.DesktopClientID,
		ClientName:    "WorkMax Desktop",
		ClientType:    model.ClientTypePublic,
		RedirectURIs:  `["http://127.0.0.1:*/oauth/callback"]`,
		AllowedScopes: `["workagent"]`,
		IsActive:      true,
	}
	if err := db.Create(&c).Error; err != nil {
		t.Fatalf("seed client: %v", err)
	}
	return &c
}
