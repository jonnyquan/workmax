package tools

// canvas_project_budget_api_test.go — P1 #6 slice 3 contract
// tests for GET/PUT /projects/:id/budget.
//
// Pins:
//   - Unauthorized (uid=0) → respondCanvasUnauthorized envelope
//   - Invalid project id → "Invalid project id"
//   - Cross-tenant IDOR → "Project not found" (same body as
//     missing-row; no enumeration oracle)
//   - GET happy path: uncapped vs capped projects project the
//     right wire shape (Cap null vs int, Remaining -1 sentinel
//     for uncapped)
//   - PUT happy path: set cap, clear cap (null), reject negative
//   - PUT missing field → "cap field is required"

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"server/globals"
	"server/model"
	systemReq "server/model/system/request"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// installSystemDB swaps globals.GraDBs["system"] for the test
// fixture so project.Default() resolves to the in-memory SQLite.
// Mirrors the pattern in workagent/agent_api_handle_chat_test.go's
// installTestDB but lives in this package to avoid cross-package
// helper leak.
func installSystemDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := globals.GraDBs
	globals.GraDBs = map[string]*gorm.DB{"system": db}
	t.Cleanup(func() { globals.GraDBs = previous })
}

func seedProjectForBudget(t *testing.T, db *gorm.DB, uid int, cap *int) uint {
	t.Helper()
	p := &model.CanvasProject{
		UID:               uid,
		UUID:              "budget-" + t.Name(),
		Title:             "fixture",
		Visibility:        0,
		BudgetCreditsCap:  cap,
		BudgetCreditsUsed: 0,
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return p.Id
}

func newBudgetCtx(t *testing.T, uid uint, projectIDParam string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if uid > 0 {
		c.Set("claims", &systemReq.CustomClaims{
			BaseClaims: systemReq.BaseClaims{Id: uid},
		})
	}
	c.Params = gin.Params{{Key: "id", Value: projectIDParam}}
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	return c, w
}

// ---------------------------------------------------------------------
// GetProjectBudget
// ---------------------------------------------------------------------

func TestGetProjectBudget_UnauthorizedWhenUIDMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	c, w := newBudgetCtx(t, 0, "1")
	api := &CanvasApi{}
	api.GetProjectBudget(c)
	if !bytes.Contains(w.Body.Bytes(), []byte("Unauthorized")) {
		t.Errorf("body should say Unauthorized, got %s", w.Body.String())
	}
}

func TestGetProjectBudget_RejectsInvalidID(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	c, w := newBudgetCtx(t, 42, "abc")
	api := &CanvasApi{}
	api.GetProjectBudget(c)
	if !bytes.Contains(w.Body.Bytes(), []byte("Invalid project id")) {
		t.Errorf("body should reject non-numeric id, got %s", w.Body.String())
	}
}

func TestGetProjectBudget_NotFoundOnCrossTenant_IDORRegression(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	// Seed under uid=100; caller is uid=42.
	cap := 100
	projID := seedProjectForBudget(t, db, 100, &cap)

	c, w := newBudgetCtx(t, 42, intToStr(projID))
	api := &CanvasApi{}
	api.GetProjectBudget(c)
	if !bytes.Contains(w.Body.Bytes(), []byte("Project not found")) {
		t.Errorf("cross-tenant must collapse to Project not found, got %s", w.Body.String())
	}
}

func TestGetProjectBudget_HappyPath_Uncapped(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	projID := seedProjectForBudget(t, db, 42, nil)

	c, w := newBudgetCtx(t, 42, intToStr(projID))
	api := &CanvasApi{}
	api.GetProjectBudget(c)

	var resp struct {
		Data budgetStatusResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Data.Cap != nil {
		t.Errorf("Cap = %v, want nil (uncapped)", resp.Data.Cap)
	}
	if resp.Data.Used != 0 {
		t.Errorf("Used = %d, want 0", resp.Data.Used)
	}
	if resp.Data.Remaining != -1 {
		t.Errorf("Remaining = %d, want -1 (uncapped sentinel)", resp.Data.Remaining)
	}
	if resp.Data.Exceeded {
		t.Errorf("Exceeded should be false for fresh project")
	}
}

func TestGetProjectBudget_HappyPath_Capped(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	cap := 100
	projID := seedProjectForBudget(t, db, 42, &cap)
	// Seed some used credits directly.
	if err := db.Model(&model.CanvasProject{}).Where("id = ?", projID).
		Update("budget_credits_used", 25).Error; err != nil {
		t.Fatal(err)
	}

	c, w := newBudgetCtx(t, 42, intToStr(projID))
	api := &CanvasApi{}
	api.GetProjectBudget(c)

	var resp struct {
		Data budgetStatusResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Cap == nil || *resp.Data.Cap != 100 {
		t.Errorf("Cap = %v, want 100", resp.Data.Cap)
	}
	if resp.Data.Used != 25 {
		t.Errorf("Used = %d, want 25", resp.Data.Used)
	}
	if resp.Data.Remaining != 75 {
		t.Errorf("Remaining = %d, want 75", resp.Data.Remaining)
	}
}

// ---------------------------------------------------------------------
// SetProjectBudget
// ---------------------------------------------------------------------

func newPutBudgetCtx(t *testing.T, uid uint, projectIDParam string, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if uid > 0 {
		c.Set("claims", &systemReq.CustomClaims{
			BaseClaims: systemReq.BaseClaims{Id: uid},
		})
	}
	c.Params = gin.Params{{Key: "id", Value: projectIDParam}}
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	return c, w
}

func TestSetProjectBudget_UnauthorizedWhenUIDMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	c, w := newPutBudgetCtx(t, 0, "1", `{"cap": 100}`)
	api := &CanvasApi{}
	api.SetProjectBudget(c)
	if !bytes.Contains(w.Body.Bytes(), []byte("Unauthorized")) {
		t.Errorf("body should say Unauthorized, got %s", w.Body.String())
	}
}

func TestSetProjectBudget_RejectsMissingCapField(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	c, w := newPutBudgetCtx(t, 42, "1", `{}`)
	api := &CanvasApi{}
	api.SetProjectBudget(c)
	if !bytes.Contains(w.Body.Bytes(), []byte("cap field is required")) {
		t.Errorf("body should require cap, got %s", w.Body.String())
	}
}

func TestSetProjectBudget_RejectsNegativeCap(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	projID := seedProjectForBudget(t, db, 42, nil)
	c, w := newPutBudgetCtx(t, 42, intToStr(projID), `{"cap": -1}`)
	api := &CanvasApi{}
	api.SetProjectBudget(c)
	// JSON marshalling HTML-escapes the `>` in ">= 0" to `>`,
	// so match on the unambiguous "must be" prefix instead.
	if !bytes.Contains(w.Body.Bytes(), []byte("budget cap must be")) {
		t.Errorf("body should reject negative cap, got %s", w.Body.String())
	}
}

func TestSetProjectBudget_NotFoundOnCrossTenant(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	projID := seedProjectForBudget(t, db, 100, nil)

	c, w := newPutBudgetCtx(t, 42, intToStr(projID), `{"cap": 50}`)
	api := &CanvasApi{}
	api.SetProjectBudget(c)
	if !bytes.Contains(w.Body.Bytes(), []byte("Project not found")) {
		t.Errorf("cross-tenant must collapse to Project not found, got %s", w.Body.String())
	}
	// Row must not have been mutated.
	var after model.CanvasProject
	if err := db.First(&after, projID).Error; err != nil {
		t.Fatal(err)
	}
	if after.BudgetCreditsCap != nil {
		t.Errorf("attacker mutated row across tenants: Cap = %v", after.BudgetCreditsCap)
	}
}

func TestSetProjectBudget_HappyPath_SetCap(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	projID := seedProjectForBudget(t, db, 42, nil)

	c, w := newPutBudgetCtx(t, 42, intToStr(projID), `{"cap": 250}`)
	api := &CanvasApi{}
	api.SetProjectBudget(c)

	// Response body should round-trip the new status.
	var resp struct {
		Data budgetStatusResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Data.Cap == nil || *resp.Data.Cap != 250 {
		t.Errorf("response Cap = %v, want 250", resp.Data.Cap)
	}
	// And persist.
	var after model.CanvasProject
	if err := db.First(&after, projID).Error; err != nil {
		t.Fatal(err)
	}
	if after.BudgetCreditsCap == nil || *after.BudgetCreditsCap != 250 {
		t.Errorf("DB Cap = %v, want 250", after.BudgetCreditsCap)
	}
}

func TestSetProjectBudget_HappyPath_ClearCap(t *testing.T) {
	// PUT {"cap": null} sets cap back to nil (unlimited).
	cap := 100
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	projID := seedProjectForBudget(t, db, 42, &cap)

	c, w := newPutBudgetCtx(t, 42, intToStr(projID), `{"cap": null}`)
	api := &CanvasApi{}
	api.SetProjectBudget(c)

	var resp struct {
		Data budgetStatusResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Data.Cap != nil {
		t.Errorf("response Cap = %v, want nil (cleared)", resp.Data.Cap)
	}
	if resp.Data.Remaining != -1 {
		t.Errorf("response Remaining = %d, want -1 after clear", resp.Data.Remaining)
	}

	var after model.CanvasProject
	if err := db.First(&after, projID).Error; err != nil {
		t.Fatal(err)
	}
	if after.BudgetCreditsCap != nil {
		t.Errorf("DB Cap = %v, want nil after null PUT", after.BudgetCreditsCap)
	}
}

// ---------------------------------------------------------------------
// Edge cases: Exceeded flag + boundary cap values. The Exceeded
// boolean is the load-bearing field for the reservation gate
// (service/account.CreditReservationService.Reserve refuses when
// Exceeded=true). If GET returns the wrong Exceeded value, a
// project either overruns silently or stays locked out
// forever — both production failure modes. Three boundaries:
//
//   1. Used > Cap → Exceeded=true, Remaining negative
//   2. Used == Cap → Exceeded=false, Remaining=0 (NOT off-by-one)
//   3. Cap=0 ("paused" project) is a valid set + projects sanely
// ---------------------------------------------------------------------

func TestGetProjectBudget_ExceededWhenUsedGreaterThanCap(t *testing.T) {
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	cap := 100
	projID := seedProjectForBudget(t, db, 42, &cap)
	if err := db.Model(&model.CanvasProject{}).Where("id = ?", projID).
		Update("budget_credits_used", 120).Error; err != nil {
		t.Fatal(err)
	}

	c, w := newBudgetCtx(t, 42, intToStr(projID))
	api := &CanvasApi{}
	api.GetProjectBudget(c)

	var resp struct {
		Data budgetStatusResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Data.Exceeded {
		t.Errorf("Exceeded = false, want true for used=120 > cap=100")
	}
	if resp.Data.Remaining != -20 {
		t.Errorf("Remaining = %d, want -20", resp.Data.Remaining)
	}
}

func TestGetProjectBudget_AtCapBoundary_NotExceeded(t *testing.T) {
	// Used == Cap → Remaining=0, Exceeded=false. The reservation
	// gate uses `Used + ceiling <= Cap`; at-cap is the LAST tick
	// before lockout, not the first locked-out state. Regression
	// guard against an off-by-one flipping the boundary.
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	cap := 100
	projID := seedProjectForBudget(t, db, 42, &cap)
	if err := db.Model(&model.CanvasProject{}).Where("id = ?", projID).
		Update("budget_credits_used", 100).Error; err != nil {
		t.Fatal(err)
	}

	c, w := newBudgetCtx(t, 42, intToStr(projID))
	api := &CanvasApi{}
	api.GetProjectBudget(c)

	var resp struct {
		Data budgetStatusResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Remaining != 0 {
		t.Errorf("Remaining = %d, want 0 at boundary", resp.Data.Remaining)
	}
	if resp.Data.Exceeded {
		t.Errorf("Exceeded = true at exact boundary, want false")
	}
}

func TestSetProjectBudget_CapZeroAccepted_PausedProject(t *testing.T) {
	// Cap=0 is a valid "paused" state — explicitly allowed by the
	// repo's >= 0 check. With used=0 the project shows Cap=0,
	// Remaining=0, Exceeded=false; any spend immediately flips
	// Exceeded=true.
	db := testutil.NewTestDB(t)
	installSystemDB(t, db)
	projID := seedProjectForBudget(t, db, 42, nil)

	c, w := newPutBudgetCtx(t, 42, intToStr(projID), `{"cap": 0}`)
	api := &CanvasApi{}
	api.SetProjectBudget(c)

	var resp struct {
		Data budgetStatusResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if resp.Data.Cap == nil || *resp.Data.Cap != 0 {
		t.Errorf("Cap = %v, want pointer to 0 (paused state)", resp.Data.Cap)
	}
	if resp.Data.Remaining != 0 {
		t.Errorf("Remaining = %d, want 0 at fresh paused state", resp.Data.Remaining)
	}
	if resp.Data.Exceeded {
		t.Errorf("Exceeded = true at used=0/cap=0, want false")
	}
}

// intToStr is the tiny formatter so the test file doesn't pull
// strconv just for this one use.
func intToStr(n uint) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 8)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
