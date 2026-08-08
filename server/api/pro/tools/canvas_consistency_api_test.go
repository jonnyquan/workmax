package tools

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"server/model"
	systemReq "server/model/system/request"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func seedProjectForConsistency(t *testing.T, db *gorm.DB, uid int) uint {
	t.Helper()
	p := &model.CanvasProject{
		UID:           uid,
		UUID:          "consistency-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Title:         "fixture",
		Visibility:    0,
		LatestVersion: 1,
		Document: model.JSONMap{
			"schemaVersion": 2,
			"elements":      []interface{}{},
			"viewport": map[string]interface{}{
				"x":     0,
				"y":     0,
				"scale": 1,
			},
		},
		SchemaVersion: 2,
		ElementCount:  0,
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return p.Id
}

func seedConsistencyMember(t *testing.T, db *gorm.DB, projectID uint, uid int, role string) {
	t.Helper()
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: projectID,
		UID:       uid,
		Role:      role,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("seed project member: %v", err)
	}
}

func newPreflightConsistencyCtx(t *testing.T, uid uint, projectID uint, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if uid > 0 {
		c.Set("claims", &systemReq.CustomClaims{
			BaseClaims: systemReq.BaseClaims{Id: uid},
		})
	}
	c.Params = gin.Params{{Key: "id", Value: intToStr(projectID)}}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	return c, w
}

func TestPreflightConsistency_AllowsOwnerAndEditorButNotViewer(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	projectID := seedProjectForConsistency(t, db, 42)
	seedConsistencyMember(t, db, projectID, 99, model.GlobalProjectRoleEditor)
	seedConsistencyMember(t, db, projectID, 100, model.GlobalProjectRoleViewer)

	cases := []struct {
		name     string
		uid      uint
		wantBody string
	}{
		{
			name:     "owner reaches prompt validation",
			uid:      42,
			wantBody: "Prompt is required",
		},
		{
			name:     "editor reaches prompt validation",
			uid:      99,
			wantBody: "Prompt is required",
		},
		{
			name:     "viewer denied",
			uid:      100,
			wantBody: "Project not found",
		},
		{
			name:     "non-member denied",
			uid:      101,
			wantBody: "Project not found",
		},
	}

	api := &CanvasApi{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, w := newPreflightConsistencyCtx(t, tc.uid, projectID, `{"prompt":""}`)
			api.PreflightConsistency(c)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantBody) {
				t.Fatalf("body = %s, want substring %q", w.Body.String(), tc.wantBody)
			}
		})
	}
}
