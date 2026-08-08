package tools

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"server/globals"
	"server/model"
	systemReq "server/model/system/request"
	"server/utils/testutil"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// applyElementPatches tests moved to
// server/service/tools/canvas/canvas_project_service_test.go under the
// exported ApplyElementPatches surface (§13 M1-W1-01).

// decodePartialJSONString tests and chat attachment validation tests
// retired with the canvas_chat_api.go handler on 2026-05-15
// (Task #15) — both stages of dead helpers had no live caller.

func TestCanvasOutpaint_InvalidExpandRatio(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("claims", &systemReq.CustomClaims{
		BaseClaims: systemReq.BaseClaims{Id: 1},
	})

	reqBody := `{"imageUrl":"https://example.com/source.png","direction":"right","expandRatio":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/tools/canvas/outpaint", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	api := &CanvasApi{}
	api.Outpaint(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse json response: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if got, _ := data["errorCode"].(string); got != canvasAIErrorInvalidExpand {
		t.Fatalf("expected errorCode %q, got %q", canvasAIErrorInvalidExpand, got)
	}
}

func TestCanvasInpaint_RequiresMaskURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("claims", &systemReq.CustomClaims{
		BaseClaims: systemReq.BaseClaims{Id: 1},
	})

	reqBody := `{"imageUrl":"https://example.com/source.png","prompt":"remove object","maskUrl":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/tools/canvas/inpaint", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	api := &CanvasApi{}
	api.Inpaint(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse json response: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if got, _ := data["errorCode"].(string); got != canvasAIErrorMaskRequired {
		t.Fatalf("expected errorCode %q, got %q", canvasAIErrorMaskRequired, got)
	}
}

func TestCanvasEditText_RequiresTargetText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("claims", &systemReq.CustomClaims{
		BaseClaims: systemReq.BaseClaims{Id: 1},
	})

	reqBody := `{"imageUrl":"https://example.com/source.png","targetText":"","replacementText":"new title"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tools/canvas/edit-text", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	api := &CanvasApi{}
	api.EditText(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse json response: %v", err)
	}
	data, _ := resp["data"].(map[string]interface{})
	if got, _ := data["errorCode"].(string); got != canvasAIErrorTextRequired {
		t.Fatalf("expected errorCode %q, got %q", canvasAIErrorTextRequired, got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// M2-W4-01: task recovery endpoint
// ─────────────────────────────────────────────────────────────────────────────

func TestParseCanvasProjectHeader(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  uint
	}{
		{"empty", "", 0},
		{"whitespace", "   ", 0},
		{"zero", "0", 0},
		{"positive", "42", 42},
		{"with spaces", "  17  ", 17},
		{"negative", "-5", 0},
		{"non-numeric", "abc", 0},
		{"float", "3.14", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			req := httptest.NewRequest(http.MethodPost, "/api/tools/canvas/img2img", nil)
			if tc.value != "" {
				req.Header.Set("X-Canvas-Project-Id", tc.value)
			}
			ctx.Request = req
			if got := parseCanvasProjectHeader(ctx); got != tc.want {
				t.Fatalf("parseCanvasProjectHeader(%q) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

func TestCanvasListTasks_UnauthorizedErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/tools/canvas/tasks", nil)

	api := &CanvasApi{}
	api.ListCanvasTasks(ctx)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", recorder.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if errorCode, _ := resp["errorCode"].(string); errorCode != canvasAIErrorUnauthorized {
		t.Fatalf("expected errorCode %s, got %#v", canvasAIErrorUnauthorized, resp["errorCode"])
	}
}

func TestCanvasListTasks_InvalidProjectIDErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("claims", &systemReq.CustomClaims{
		BaseClaims: systemReq.BaseClaims{Id: 1},
	})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/tools/canvas/tasks?projectId=not-a-number", nil)

	api := &CanvasApi{}
	api.ListCanvasTasks(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if errorCode, _ := resp["errorCode"].(string); errorCode != "INVALID_PROJECT_ID" {
		t.Fatalf("expected errorCode INVALID_PROJECT_ID, got %#v", resp["errorCode"])
	}
}

func TestCanvasListTasks_InvalidStatusFilterErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("claims", &systemReq.CustomClaims{
		BaseClaims: systemReq.BaseClaims{Id: 1},
	})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/tools/canvas/tasks?status=bogus", nil)

	api := &CanvasApi{}
	api.ListCanvasTasks(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if errorCode, _ := resp["errorCode"].(string); errorCode != "INVALID_TASK_STATUS_FILTER" {
		t.Fatalf("expected errorCode INVALID_TASK_STATUS_FILTER, got %#v", resp["errorCode"])
	}
}

func TestCanvasListTasks_ProjectMemberSeesCollaboratorTasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	previousDBs := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() {
		globals.GraDBs = previousDBs
	})
	if err := db.Create(&model.CanvasProject{
		UID:      42,
		UUID:     "task-project",
		Title:    "Shared",
		Document: model.JSONMap{},
	}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: 1,
		UID:       99,
		Role:      model.GlobalProjectRoleViewer,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("seed member: %v", err)
	}
	if err := db.Create(&model.GenerationTask{
		TaskID: "canvas-task-editor",
		UID:    77,
		ToolID: "image-generator",
		Model:  "test-model",
		Status: model.TaskStatusProcessing,
	}).Error; err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if err := db.Create(&model.CanvasTaskBinding{
		UID:       77,
		ProjectID: 1,
		TaskID:    "canvas-task-editor",
		ElementID: "el-1",
	}).Error; err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("claims", &systemReq.CustomClaims{
		BaseClaims: systemReq.BaseClaims{Id: 99},
	})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/tools/canvas/tasks?projectId=1", nil)

	api := &CanvasApi{}
	api.ListCanvasTasks(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				TaskID    string `json:"taskId"`
				ElementID string `json:"elementId"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if len(resp.Data.Items) != 1 || resp.Data.Items[0].TaskID != "canvas-task-editor" {
		t.Fatalf("items = %#v, want collaborator task", resp.Data.Items)
	}
}

func TestCanvasAgentThreads_ProjectMemberSeesProjectThreads(t *testing.T) {
	db := testutil.NewTestDB(t)
	previousDBs := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() {
		globals.GraDBs = previousDBs
	})
	if err := db.Create(&model.CanvasProject{
		UID:      42,
		UUID:     "agent-project",
		Title:    "Shared",
		Document: model.JSONMap{},
	}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: 1,
		UID:       99,
		Role:      model.GlobalProjectRoleViewer,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("seed member: %v", err)
	}
	now := "2026-01-01 00:00:00"
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (id, uid, uuid, project_id, agent_type, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		7001, 77, "thread-project", 1, "canvas", "Project thread", now, now,
	).Error; err != nil {
		t.Fatalf("seed project thread: %v", err)
	}
	if err := db.Exec(
		`INSERT INTO w_workagent_message (id, uid, uuid, thread_id, user_text, ai_text, chat_mode, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		8001, 77, "msg-project", 7001, "generate", "done", "image", now, now,
	).Error; err != nil {
		t.Fatalf("seed project message: %v", err)
	}

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("claims", &systemReq.CustomClaims{
		BaseClaims: systemReq.BaseClaims{Id: 99},
	})
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/tools/canvas/agent/threads?projectId=1", nil)

	api := &CanvasApi{}
	api.AgentThreads(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var threadsResp struct {
		Threads []struct {
			Id        uint `json:"id"`
			ProjectID uint `json:"projectId"`
		} `json:"threads"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &threadsResp); err != nil {
		t.Fatalf("bad thread json: %v", err)
	}
	if len(threadsResp.Threads) != 1 || threadsResp.Threads[0].Id != 7001 {
		t.Fatalf("threads = %#v, want project thread", threadsResp.Threads)
	}

	messagesRecorder := httptest.NewRecorder()
	messagesCtx, _ := gin.CreateTestContext(messagesRecorder)
	messagesCtx.Set("claims", &systemReq.CustomClaims{
		BaseClaims: systemReq.BaseClaims{Id: 99},
	})
	messagesCtx.Params = gin.Params{{Key: "threadId", Value: "7001"}}
	messagesCtx.Request = httptest.NewRequest(http.MethodGet, "/api/tools/canvas/agent/threads/7001/messages?projectId=1", nil)

	api.AgentMessages(messagesCtx)

	if messagesRecorder.Code != http.StatusOK {
		t.Fatalf("expected messages status 200, got %d body=%s", messagesRecorder.Code, messagesRecorder.Body.String())
	}
	var messagesResp struct {
		Messages []struct {
			Id     uint   `json:"id"`
			AIText string `json:"aiText"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(messagesRecorder.Body.Bytes(), &messagesResp); err != nil {
		t.Fatalf("bad message json: %v", err)
	}
	if len(messagesResp.Messages) != 1 || messagesResp.Messages[0].Id != 8001 {
		t.Fatalf("messages = %#v, want project message", messagesResp.Messages)
	}
}

func TestAppendAgentMessage_ProjectEditorCanAppendSharedThread(t *testing.T) {
	db := testutil.NewTestDB(t)
	previousDBs := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() {
		globals.GraDBs = previousDBs
	})
	if err := db.Create(&model.CanvasProject{
		UID:      42,
		UUID:     "append-project",
		Title:    "Shared",
		Document: model.JSONMap{},
	}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	for _, member := range []model.GlobalProjectMember{
		{ProjectID: 1, UID: 99, Role: model.GlobalProjectRoleEditor, Source: model.GlobalProjectMemberSourceInvite, CreatedBy: 42},
		{ProjectID: 1, UID: 88, Role: model.GlobalProjectRoleViewer, Source: model.GlobalProjectMemberSourceInvite, CreatedBy: 42},
	} {
		if err := db.Create(&member).Error; err != nil {
			t.Fatalf("seed member: %v", err)
		}
	}
	now := "2026-01-01 00:00:00"
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (id, uid, uuid, project_id, agent_type, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		7001, 77, "thread-project", 1, "canvas", "Project thread", now, now,
	).Error; err != nil {
		t.Fatalf("seed project thread: %v", err)
	}

	api := &CanvasApi{}
	gin.SetMode(gin.TestMode)
	body := []byte(`{"conversationId":"thread-project","projectId":"1","userText":"generate image","chatMode":"image","contentType":"canvas_direct_generation"}`)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("claims", &systemReq.CustomClaims{
		BaseClaims: systemReq.BaseClaims{Id: 99},
	})
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/tools/canvas/agent/messages", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	api.AppendAgentMessage(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected editor append status 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var okResp struct {
		Message struct {
			ThreadID int `json:"threadId"`
			UID      int `json:"uid"`
		} `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &okResp); err != nil {
		t.Fatalf("bad append json: %v", err)
	}
	if okResp.Message.ThreadID != 7001 || okResp.Message.UID != 99 {
		t.Fatalf("message = %#v, want editor message on shared thread", okResp.Message)
	}

	denyRecorder := httptest.NewRecorder()
	denyCtx, _ := gin.CreateTestContext(denyRecorder)
	denyCtx.Set("claims", &systemReq.CustomClaims{
		BaseClaims: systemReq.BaseClaims{Id: 88},
	})
	denyCtx.Request = httptest.NewRequest(http.MethodPost, "/api/tools/canvas/agent/messages", bytes.NewReader(body))
	denyCtx.Request.Header.Set("Content-Type", "application/json")

	api.AppendAgentMessage(denyCtx)

	if denyRecorder.Code != http.StatusBadRequest {
		t.Fatalf("expected viewer append status 400, got %d body=%s", denyRecorder.Code, denyRecorder.Body.String())
	}
}

// TestBuildCanvasTaskRecoveryMeta_NumericCoercion pins the wire-shape
// contract for the recovery meta endpoint (P1-⑦). The FE's
// CanvasRecoveredTask declares duration/width/height as `number`; any
// string-encoded numeric leaking through becomes silent string concat
// in arithmetic. params.* may arrive as float64 (canonical JSON) or
// as a quoted string from clients that serialize numerics — both
// must surface to the FE as a number.
func TestBuildCanvasTaskRecoveryMeta_NumericCoercion(t *testing.T) {
	cases := []struct {
		name   string
		params model.JSONMap
		want   map[string]float64
	}{
		{
			name:   "float64_passthrough",
			params: model.JSONMap{"duration": 8.0, "width": 1024.0, "height": 1024.0},
			want:   map[string]float64{"duration": 8, "width": 1024, "height": 1024},
		},
		{
			name:   "int_widened_to_float64",
			params: model.JSONMap{"width": int(1024), "height": int64(768)},
			want:   map[string]float64{"width": 1024, "height": 768},
		},
		{
			name:   "string_coerced_to_float64",
			params: model.JSONMap{"duration": "8", "width": "1024", "height": " 768 "},
			want:   map[string]float64{"duration": 8, "width": 1024, "height": 768},
		},
		{
			name:   "garbage_string_dropped",
			params: model.JSONMap{"width": "wide", "height": ""},
			want:   map[string]float64{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := buildCanvasTaskRecoveryMeta(model.JSONMap{"params": tc.params})
			for key, want := range tc.want {
				got, ok := out[key]
				if !ok {
					t.Errorf("%s: missing %s in meta", tc.name, key)
					continue
				}
				gotFloat, ok := got.(float64)
				if !ok {
					t.Errorf("%s: %s wire-shape leak — got %T (%v), want float64", tc.name, key, got, got)
					continue
				}
				if gotFloat != want {
					t.Errorf("%s: %s = %v, want %v", tc.name, key, gotFloat, want)
				}
			}
			for _, key := range []string{"duration", "width", "height"} {
				if _, expected := tc.want[key]; !expected {
					if _, leaked := out[key]; leaked {
						t.Errorf("%s: %s unexpectedly present (%v); should be dropped", tc.name, key, out[key])
					}
				}
			}
		})
	}
}
