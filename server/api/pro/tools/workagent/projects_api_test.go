package workagent

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	canvasService "server/service/tools/canvas"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
)

func buildProjectEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.GET("/projects/:projectId/settings", withClaims(uid), api.GetProjectSettings)
	r.PATCH("/projects/:projectId/settings", withClaims(uid), api.UpdateProjectSettings)
	return r
}

func TestGetProjectSettings_ReturnsWorkAgentDTOOnly(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	project, err := canvasService.CreateProject(context.Background(), db, 42, canvasService.CreateProjectInput{
		Title: "Campaign",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	engine := buildProjectEngine(t, 42)
	w := getRequest(engine, "/projects/"+uintToStr(project.Id)+"/settings")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp.Data["title"]; got != "Campaign" {
		t.Errorf("title = %v, want Campaign", got)
	}
	if _, leaked := resp.Data["document"]; leaked {
		t.Errorf("settings DTO leaked Canvas document")
	}
	if _, leaked := resp.Data["uid"]; leaked {
		t.Errorf("settings DTO leaked owner uid")
	}
}

func TestUpdateProjectSettings_ReturnsWorkAgentDTOOnly(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	project, err := canvasService.CreateProject(context.Background(), db, 42, canvasService.CreateProjectInput{
		Title: "Campaign",
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	engine := buildProjectEngine(t, 42)
	w := patchJSON(engine, "/projects/"+uintToStr(project.Id)+"/settings", map[string]any{
		"title": "Launch Plan",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp.Data["title"]; got != "Launch Plan" {
		t.Errorf("title = %v, want Launch Plan", got)
	}
	if _, leaked := resp.Data["document"]; leaked {
		t.Errorf("settings DTO leaked Canvas document")
	}
	if _, leaked := resp.Data["uid"]; leaked {
		t.Errorf("settings DTO leaked owner uid")
	}
}
