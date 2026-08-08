package workagent

// skill_catalog_api_test.go — F3 (2026-05-17). Covers:
//   - ListSkills shape (items + count + per-item fields)
//   - Catalog size matches allowedAgentModes (14 entries today)
//   - Alphabetical ordering (FE picker depends on stable order)
//   - Per-skill bundle metadata (version + has* booleans) reaches
//     the wire — pins ppt as the load-bearing case
//   - Unauthorized when uid=0

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	agentv1 "server/contracts/agent/v1"
	"server/globals"
	"server/model"
	workagentModel "server/model/workagent"
	workagentService "server/service/tools/workagent"
	"server/service/tools/workagent/skills"
	"server/utils/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func buildSkillCatalogEngine(_ *testing.T, uid uint) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	api := NewAIChatApiNew()
	r.GET("/skills", withClaims(uid), api.ListSkills)
	r.POST("/skills/:agentMode/access-requests", withClaims(uid), api.RequestSkillAccess)
	r.GET("/skills/access-requests", withClaims(uid), api.ListSkillAccessRequests)
	r.PATCH("/skills/access-requests/:requestId/status", withClaims(uid), api.UpdateSkillAccessRequestStatus)
	r.GET("/design-systems", withClaims(uid), api.ListDesignSystems)
	r.PATCH("/projects/:projectId/design-systems/:designSystemId/status", withClaims(uid), api.UpdateProjectDesignSystemStatus)
	r.GET("/projects/:projectId/design-systems/:designSystemId/history", withClaims(uid), api.GetProjectDesignSystemHistory)
	r.POST("/projects/:projectId/design-systems/fork", withClaims(uid), api.ForkOfficialDesignSystem)
	r.POST("/projects/:projectId/design-systems/:designSystemId/fork", withClaims(uid), api.ForkProjectDesignSystem)
	return r
}

func seedWorkAgentProjectOwner(t *testing.T, db *gorm.DB, projectID uint, uid uint) {
	t.Helper()
	if err := db.Create(&model.CanvasProject{
		GraMODEL: globals.GraMODEL{Id: projectID},
		UID:      int(uid),
		UUID:     "project-owner-test",
		Title:    "Project Owner Test",
	}).Error; err != nil {
		t.Fatalf("seed project owner: %v", err)
	}
}

func seedWorkAgentProjectMember(t *testing.T, db *gorm.DB, projectID uint, uid uint, role string) {
	t.Helper()
	if err := db.Create(&model.GlobalProjectMember{
		ProjectID: projectID,
		UID:       int(uid),
		Role:      role,
		Source:    model.GlobalProjectMemberSourceInvite,
		CreatedBy: 42,
	}).Error; err != nil {
		t.Fatalf("seed project member: %v", err)
	}
}

func seedWorkAgentPremiumUser(t *testing.T, db *gorm.DB, uid uint) {
	t.Helper()
	if err := db.Create(&model.User{
		GraMODEL:      globals.GraMODEL{Id: uid},
		Email:         "premium@example.com",
		Member:        model.MEMBER_SUBSCRIPTION_PRO,
		MemberEndTime: time.Now().Add(24 * time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed premium user: %v", err)
	}
}

func seedWorkAgentTeamMember(t *testing.T, db *gorm.DB, teamID uint64, uid uint) {
	t.Helper()
	if err := db.Create(&model.TeamMember{
		TeamID: teamID,
		UID:    int(uid),
		Role:   model.TeamMemberRoleMember,
		Status: model.TeamMemberStatusActive,
	}).Error; err != nil {
		t.Fatalf("seed team member: %v", err)
	}
}

func TestListSkills_UnauthorizedWhenUIDMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 0)

	w := getRequest(engine, "/skills")
	if !strings.Contains(w.Body.String(), "Unauthorized") {
		t.Errorf("body should say Unauthorized, got %q", w.Body.String())
	}
}

func TestListSkills_ReturnsAllUserFacingSkills(t *testing.T) {
	// The catalog must enumerate every entry in allowedAgentModes.
	// A regression that filters one out would silently disappear
	// a skill from the FE picker.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			CatalogVersion string             `json:"catalogVersion"`
			Items          []skillCatalogItem `json:"items"`
			Count          int                `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Count != len(allowedAgentModes) {
		t.Errorf("count = %d, want %d (allowedAgentModes size)", body.Data.Count, len(allowedAgentModes))
	}
	if len(body.Data.Items) != len(allowedAgentModes) {
		t.Errorf("items len = %d, want %d", len(body.Data.Items), len(allowedAgentModes))
	}
	if body.Data.CatalogVersion != agentv1.SkillCatalogVersion {
		t.Errorf("catalogVersion = %q, want %q", body.Data.CatalogVersion, agentv1.SkillCatalogVersion)
	}

	// Every allowedAgentModes entry must appear exactly once.
	got := make(map[string]int, len(body.Data.Items))
	for _, it := range body.Data.Items {
		got[it.AgentMode]++
		if strings.TrimSpace(it.Name) == "" || strings.TrimSpace(it.Description) == "" {
			t.Errorf("skill %q missing server-owned display metadata: %#v", it.AgentMode, it)
		}
	}
	for mode := range allowedAgentModes {
		if got[mode] != 1 {
			t.Errorf("skill %q appears %d times in catalog, want 1", mode, got[mode])
		}
	}
}

func TestRequestSkillAccess_CreatesPendingRequestAndCatalogEchoesStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := postJSON(engine, "/skills/ppt/access-requests", map[string]any{
		"reason": "Need to test marketplace approval flow",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Data struct {
			RequestID uint   `json:"request_id"`
			AgentMode string `json:"agent_mode"`
			Status    string `json:"status"`
			Source    string `json:"source"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if created.Data.RequestID == 0 || created.Data.AgentMode != "ppt" || created.Data.Status != workagentModel.SkillAccessRequestStatusPending || created.Data.Source != "official" {
		t.Fatalf("created request = %#v", created.Data)
	}

	w = getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode catalog: %v body=%s", err, w.Body.String())
	}
	var ppt *skillCatalogItem
	for i := range body.Data.Items {
		if body.Data.Items[i].AgentMode == "ppt" {
			ppt = &body.Data.Items[i]
			break
		}
	}
	if ppt == nil || ppt.AccessRequestID != created.Data.RequestID || ppt.AccessRequestStatus != workagentModel.SkillAccessRequestStatusPending {
		t.Fatalf("ppt catalog access request = %#v, want pending id=%d", ppt, created.Data.RequestID)
	}
}

func TestListSkills_AppliesConfiguredAccessGate(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:marketplace")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode catalog: %v body=%s", err, w.Body.String())
	}
	var productShot *skillCatalogItem
	for i := range body.Data.Items {
		if body.Data.Items[i].AgentMode == "productShot" {
			productShot = &body.Data.Items[i]
			break
		}
	}
	if productShot == nil {
		t.Fatalf("productShot missing from catalog")
	}
	if productShot.Source != "marketplace" || !stringSliceEqual(productShot.Permissions, []string{"request_access"}) {
		t.Fatalf("gated productShot item = %#v", productShot)
	}
}

func TestListSkills_AppliesConfiguredPaidTierGate(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:paid:pro")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode catalog: %v body=%s", err, w.Body.String())
	}
	for _, item := range body.Data.Items {
		if item.AgentMode != "productShot" {
			continue
		}
		if item.Source != "paid" || item.RequiredTier != "pro" || !stringSliceEqual(item.Permissions, []string{"request_access"}) {
			t.Fatalf("paid gated productShot item = %#v", item)
		}
		return
	}
	t.Fatalf("productShot missing from catalog")
}

func TestListSkills_AppliesConfiguredUnpublishedStatus(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_SKILL_STATUSES", "productShot:unpublished")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode catalog: %v body=%s", err, w.Body.String())
	}
	for _, item := range body.Data.Items {
		if item.AgentMode != "productShot" {
			continue
		}
		if item.Status != "unpublished" || len(item.Permissions) != 0 {
			t.Fatalf("unpublished productShot item = %#v", item)
		}
		return
	}
	t.Fatalf("productShot missing from catalog")
}

func TestListSkills_UnlocksPaidTierGateForPremiumUser(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:paid:pro")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	seedWorkAgentPremiumUser(t, db, 42)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode catalog: %v body=%s", err, w.Body.String())
	}
	for _, item := range body.Data.Items {
		if item.AgentMode != "productShot" {
			continue
		}
		if item.Source != "paid" || item.RequiredTier != "pro" || !stringSliceContains(item.Permissions, "use") {
			t.Fatalf("premium paid gated productShot item = %#v", item)
		}
		return
	}
	t.Fatalf("productShot missing from catalog")
}

func TestRequestSkillAccess_UsesConfiguredGateSourceAndApprovalUnlocksUse(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:gray")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := postJSON(engine, "/skills/productShot/access-requests", map[string]any{"reason": "beta launch"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Data struct {
			RequestID uint   `json:"request_id"`
			Source    string `json:"source"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode request: %v body=%s", err, w.Body.String())
	}
	if created.Data.RequestID == 0 || created.Data.Source != "gray" {
		t.Fatalf("created gated request = %#v", created.Data)
	}
	if err := db.Model(&workagentModel.SkillAccessRequest{}).
		Where("id = ?", created.Data.RequestID).
		Update("status", workagentModel.SkillAccessRequestStatusApproved).Error; err != nil {
		t.Fatalf("approve request: %v", err)
	}
	w = getRequest(engine, "/skills")
	var body struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode catalog: %v body=%s", err, w.Body.String())
	}
	for _, item := range body.Data.Items {
		if item.AgentMode != "productShot" {
			continue
		}
		if item.Source != "gray" || item.AccessRequestStatus != workagentModel.SkillAccessRequestStatusApproved || !stringSliceContains(item.Permissions, "use") {
			t.Fatalf("approved gated catalog item = %#v", item)
		}
		return
	}
	t.Fatalf("productShot missing from gated catalog")
}

func TestSkillCatalogOfficialManifestPreflightAndRuntimeGateStayAligned(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:marketplace")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	bundle, err := workagentService.GetSkillRegistry().Build("productShot", skills.BuildContext{})
	if err != nil {
		t.Fatalf("build productShot bundle: %v", err)
	}
	if bundle.Artifacts == nil || bundle.Artifacts.PrimaryType != "product_image" {
		t.Fatalf("productShot manifest artifacts = %#v", bundle.Artifacts)
	}

	w := getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d body=%s", w.Code, w.Body.String())
	}
	var catalog struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode catalog: %v body=%s", err, w.Body.String())
	}
	item := findSkillCatalogTestItem(catalog.Data.Items, "productShot")
	if item == nil {
		t.Fatalf("productShot missing from catalog")
	}
	if item.Source != "marketplace" || item.Status != "published" || !stringSliceEqual(item.Permissions, []string{"request_access"}) {
		t.Fatalf("gated catalog governance = %#v", item)
	}
	if item.Artifacts == nil || item.Artifacts.PrimaryType != bundle.Artifacts.PrimaryType ||
		!stringSliceEqual(item.Artifacts.OutputTypes, bundle.Artifacts.OutputTypes) ||
		!stringSliceEqual(item.Artifacts.ExportTargets, bundle.Artifacts.ExportTargets) {
		t.Fatalf("catalog artifacts = %#v, want manifest %#v", item.Artifacts, bundle.Artifacts)
	}
	if !hasInputKind(item.RequiredInputs, "product_reference_image") ||
		!containsString(item.RiskHints, "reference_asset_sensitive") ||
		!containsString(item.RiskHints, "visual_fidelity_review") {
		t.Fatalf("catalog did not expose manifest-driven inputs/risk hints: inputs=%#v hints=%#v", item.RequiredInputs, item.RiskHints)
	}

	preflight := workagentService.BuildPreflightAdditionsForThread(42, "productShot", 0)
	for _, want := range []string{
		"<skill-checklist>",
		"<skill-side-files>",
		"composition-grid.md",
		"Product missing",
	} {
		if !strings.Contains(preflight, want) {
			t.Fatalf("productShot preflight missing %q; got %q", want, preflight)
		}
	}

	decision, err := ensureSkillRuntimeAccess(42, "productShot")
	if err != nil {
		t.Fatalf("runtime gate before approval: %v", err)
	}
	if decision.Allowed || decision.Source != "marketplace" || !strings.Contains(decision.Message, "requires approval") {
		t.Fatalf("runtime gate before approval = %#v", decision)
	}

	w = postJSON(engine, "/skills/productShot/access-requests", map[string]any{"reason": "launch product photography workflow"})
	if w.Code != http.StatusOK {
		t.Fatalf("request access status = %d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Data struct {
			RequestID uint `json:"request_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode access request: %v body=%s", err, w.Body.String())
	}
	if _, err := workagentService.NewSkillAccessRequestRepository(db).UpdateStatus(created.Data.RequestID, workagentModel.SkillAccessRequestStatusApproved, 1, "Approved fixture"); err != nil {
		t.Fatalf("approve access request: %v", err)
	}

	w = getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("catalog after approval status = %d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode catalog after approval: %v body=%s", err, w.Body.String())
	}
	item = findSkillCatalogTestItem(catalog.Data.Items, "productShot")
	if item == nil || item.AccessRequestStatus != workagentModel.SkillAccessRequestStatusApproved || !stringSliceContains(item.Permissions, "use") {
		t.Fatalf("approved catalog item = %#v", item)
	}
	decision, err = ensureSkillRuntimeAccess(42, "productShot")
	if err != nil {
		t.Fatalf("runtime gate after approval: %v", err)
	}
	if !decision.Allowed || decision.Source != "marketplace" {
		t.Fatalf("runtime gate after approval = %#v", decision)
	}
}

func TestSkillCatalogHTMLNativeManifestPreflightAndRuntimeGateStayAligned(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "webBanner:marketplace")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	bundle, err := workagentService.GetSkillRegistry().Build("webBanner", skills.BuildContext{})
	if err != nil {
		t.Fatalf("build webBanner bundle: %v", err)
	}
	if bundle.Artifacts == nil || bundle.Artifacts.PrimaryType != "web_banner" {
		t.Fatalf("webBanner manifest artifacts = %#v", bundle.Artifacts)
	}

	w := getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d body=%s", w.Code, w.Body.String())
	}
	var catalog struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode catalog: %v body=%s", err, w.Body.String())
	}
	item := findSkillCatalogTestItem(catalog.Data.Items, "webBanner")
	if item == nil {
		t.Fatalf("webBanner missing from catalog")
	}
	if item.Source != "marketplace" || item.Status != "published" || !stringSliceEqual(item.Permissions, []string{"request_access"}) {
		t.Fatalf("gated catalog governance = %#v", item)
	}
	if item.Artifacts == nil || item.Artifacts.PrimaryType != bundle.Artifacts.PrimaryType ||
		!stringSliceEqual(item.Artifacts.OutputTypes, bundle.Artifacts.OutputTypes) ||
		!stringSliceEqual(item.Artifacts.ExportTargets, bundle.Artifacts.ExportTargets) {
		t.Fatalf("catalog artifacts = %#v, want manifest %#v", item.Artifacts, bundle.Artifacts)
	}
	for _, want := range []string{
		"html_static_validation",
		"motion_export_review",
		"visual_fidelity_review",
		"compliance_review",
	} {
		if !containsString(item.RiskHints, want) {
			t.Fatalf("webBanner risk hints = %#v, missing %q", item.RiskHints, want)
		}
	}
	for _, want := range []string{"banner_size", "style_direction", "cta_text", "headline_text"} {
		if !hasInputKind(item.RequiredInputs, want) {
			t.Fatalf("webBanner required inputs = %#v, missing %q", item.RequiredInputs, want)
		}
	}

	preflight := workagentService.BuildPreflightAdditionsForThread(42, "webBanner", 0)
	for _, want := range []string{
		"<skill-checklist>",
		"<skill-side-files>",
		`<asset path="asset-contract-usage.md">`,
		`<asset path="html-native-template-seed.md">`,
		`<asset path="html-motion-helper.md">`,
		"Export Readiness",
		"reduced-motion",
	} {
		if !strings.Contains(preflight, want) {
			t.Fatalf("webBanner preflight missing %q; got %q", want, preflight)
		}
	}

	decision, err := ensureSkillRuntimeAccess(42, "webBanner")
	if err != nil {
		t.Fatalf("runtime gate before approval: %v", err)
	}
	if decision.Allowed || decision.Source != "marketplace" || !strings.Contains(decision.Message, "requires approval") {
		t.Fatalf("runtime gate before approval = %#v", decision)
	}

	w = postJSON(engine, "/skills/webBanner/access-requests", map[string]any{"reason": "launch HTML-native banner workflow"})
	if w.Code != http.StatusOK {
		t.Fatalf("request access status = %d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Data struct {
			RequestID uint `json:"request_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode access request: %v body=%s", err, w.Body.String())
	}
	if _, err := workagentService.NewSkillAccessRequestRepository(db).UpdateStatus(created.Data.RequestID, workagentModel.SkillAccessRequestStatusApproved, 1, "Approved HTML-native fixture"); err != nil {
		t.Fatalf("approve access request: %v", err)
	}

	w = getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("catalog after approval status = %d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode catalog after approval: %v body=%s", err, w.Body.String())
	}
	item = findSkillCatalogTestItem(catalog.Data.Items, "webBanner")
	if item == nil || item.AccessRequestStatus != workagentModel.SkillAccessRequestStatusApproved || !stringSliceContains(item.Permissions, "use") {
		t.Fatalf("approved catalog item = %#v", item)
	}
	decision, err = ensureSkillRuntimeAccess(42, "webBanner")
	if err != nil {
		t.Fatalf("runtime gate after approval: %v", err)
	}
	if !decision.Allowed || decision.Source != "marketplace" {
		t.Fatalf("runtime gate after approval = %#v", decision)
	}
}

func TestSkillCatalogMotionManifestPreflightAndRuntimeGateStayAligned(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "socialAd:marketplace")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	bundle, err := workagentService.GetSkillRegistry().Build("socialAd", skills.BuildContext{})
	if err != nil {
		t.Fatalf("build socialAd bundle: %v", err)
	}
	if bundle.Artifacts == nil || bundle.Artifacts.PrimaryType != "social_ad" {
		t.Fatalf("socialAd manifest artifacts = %#v", bundle.Artifacts)
	}

	w := getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status = %d body=%s", w.Code, w.Body.String())
	}
	var catalog struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode catalog: %v body=%s", err, w.Body.String())
	}
	item := findSkillCatalogTestItem(catalog.Data.Items, "socialAd")
	if item == nil {
		t.Fatalf("socialAd missing from catalog")
	}
	if item.Source != "marketplace" || item.Status != "published" || !stringSliceEqual(item.Permissions, []string{"request_access"}) {
		t.Fatalf("gated catalog governance = %#v", item)
	}
	if item.Artifacts == nil || item.Artifacts.PrimaryType != bundle.Artifacts.PrimaryType ||
		!stringSliceEqual(item.Artifacts.OutputTypes, bundle.Artifacts.OutputTypes) ||
		!stringSliceEqual(item.Artifacts.ExportTargets, bundle.Artifacts.ExportTargets) {
		t.Fatalf("catalog artifacts = %#v, want manifest %#v", item.Artifacts, bundle.Artifacts)
	}
	for _, want := range []string{
		"reference_asset_sensitive",
		"motion_export_review",
		"visual_fidelity_review",
	} {
		if !containsString(item.RiskHints, want) {
			t.Fatalf("socialAd risk hints = %#v, missing %q", item.RiskHints, want)
		}
	}
	for _, want := range []string{"target_platform", "aspect_ratio", "concept_or_product", "brand_assets", "cta_text"} {
		if !hasInputKind(item.RequiredInputs, want) {
			t.Fatalf("socialAd required inputs = %#v, missing %q", item.RequiredInputs, want)
		}
	}

	preflight := workagentService.BuildPreflightAdditionsForThread(42, "socialAd", 0)
	for _, want := range []string{
		"<skill-checklist>",
		"<skill-side-files>",
		`<asset path="asset-contract-usage.md">`,
		`<asset path="html-native-template-seed.md">`,
		`<asset path="html-motion-helper.md">`,
		"motion_pacing",
		"reduced-motion",
	} {
		if !strings.Contains(preflight, want) {
			t.Fatalf("socialAd preflight missing %q; got %q", want, preflight)
		}
	}

	decision, err := ensureSkillRuntimeAccess(42, "socialAd")
	if err != nil {
		t.Fatalf("runtime gate before approval: %v", err)
	}
	if decision.Allowed || decision.Source != "marketplace" || !strings.Contains(decision.Message, "requires approval") {
		t.Fatalf("runtime gate before approval = %#v", decision)
	}

	w = postJSON(engine, "/skills/socialAd/access-requests", map[string]any{"reason": "launch motion social ad workflow"})
	if w.Code != http.StatusOK {
		t.Fatalf("request access status = %d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		Data struct {
			RequestID uint `json:"request_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode access request: %v body=%s", err, w.Body.String())
	}
	if _, err := workagentService.NewSkillAccessRequestRepository(db).UpdateStatus(created.Data.RequestID, workagentModel.SkillAccessRequestStatusApproved, 1, "Approved motion fixture"); err != nil {
		t.Fatalf("approve access request: %v", err)
	}

	w = getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("catalog after approval status = %d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatalf("decode catalog after approval: %v body=%s", err, w.Body.String())
	}
	item = findSkillCatalogTestItem(catalog.Data.Items, "socialAd")
	if item == nil || item.AccessRequestStatus != workagentModel.SkillAccessRequestStatusApproved || !stringSliceContains(item.Permissions, "use") {
		t.Fatalf("approved catalog item = %#v", item)
	}
	decision, err = ensureSkillRuntimeAccess(42, "socialAd")
	if err != nil {
		t.Fatalf("runtime gate after approval: %v", err)
	}
	if !decision.Allowed || decision.Source != "marketplace" {
		t.Fatalf("runtime gate after approval = %#v", decision)
	}
}

func TestListSkills_AppliesGrayReleaseCohort(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:gray")
	t.Setenv("WORKMAX_WORKAGENT_GRAY_SKILL_USERS", "productShot:42|7")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	allowedEngine := buildSkillCatalogEngine(t, 42)
	w := getRequest(allowedEngine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("allowed status = %d body=%s", w.Code, w.Body.String())
	}
	var allowed struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &allowed); err != nil {
		t.Fatalf("decode allowed catalog: %v body=%s", err, w.Body.String())
	}
	allowedItem := findSkillCatalogTestItem(allowed.Data.Items, "productShot")
	if allowedItem == nil || allowedItem.Source != "gray" || allowedItem.Status != "published" || !stringSliceEqual(allowedItem.Permissions, []string{"request_access"}) {
		t.Fatalf("allowed gray item = %#v", allowedItem)
	}

	blockedEngine := buildSkillCatalogEngine(t, 99)
	w = getRequest(blockedEngine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("blocked status = %d body=%s", w.Code, w.Body.String())
	}
	var blocked struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &blocked); err != nil {
		t.Fatalf("decode blocked catalog: %v body=%s", err, w.Body.String())
	}
	blockedItem := findSkillCatalogTestItem(blocked.Data.Items, "productShot")
	if blockedItem == nil || blockedItem.Source != "gray" || blockedItem.Status != "unpublished" || len(blockedItem.Permissions) != 0 {
		t.Fatalf("blocked gray item = %#v", blockedItem)
	}
}

func TestListSkills_AppliesGrayReleaseProjectCohort(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:gray")
	t.Setenv("WORKMAX_WORKAGENT_GRAY_SKILL_PROJECTS", "productShot:77")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/skills?projectId=77")
	if w.Code != http.StatusOK {
		t.Fatalf("allowed project status = %d body=%s", w.Code, w.Body.String())
	}
	var allowed struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &allowed); err != nil {
		t.Fatalf("decode allowed project catalog: %v body=%s", err, w.Body.String())
	}
	allowedItem := findSkillCatalogTestItem(allowed.Data.Items, "productShot")
	if allowedItem == nil || allowedItem.Source != "gray" || allowedItem.Status != "published" || !stringSliceEqual(allowedItem.Permissions, []string{"request_access"}) {
		t.Fatalf("allowed project gray item = %#v", allowedItem)
	}

	w = getRequest(engine, "/skills?project_id=88")
	if w.Code != http.StatusOK {
		t.Fatalf("blocked project status = %d body=%s", w.Code, w.Body.String())
	}
	var blocked struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &blocked); err != nil {
		t.Fatalf("decode blocked project catalog: %v body=%s", err, w.Body.String())
	}
	blockedItem := findSkillCatalogTestItem(blocked.Data.Items, "productShot")
	if blockedItem == nil || blockedItem.Source != "gray" || blockedItem.Status != "unpublished" || len(blockedItem.Permissions) != 0 {
		t.Fatalf("blocked project gray item = %#v", blockedItem)
	}
}

func TestListSkills_AppliesGrayReleaseTeamCohort(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:gray")
	t.Setenv("WORKMAX_WORKAGENT_GRAY_SKILL_TEAMS", "productShot:7001")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	seedWorkAgentTeamMember(t, db, 7001, 42)

	allowedEngine := buildSkillCatalogEngine(t, 42)
	w := getRequest(allowedEngine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("allowed team status = %d body=%s", w.Code, w.Body.String())
	}
	var allowed struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &allowed); err != nil {
		t.Fatalf("decode allowed team catalog: %v body=%s", err, w.Body.String())
	}
	allowedItem := findSkillCatalogTestItem(allowed.Data.Items, "productShot")
	if allowedItem == nil || allowedItem.Source != "gray" || allowedItem.Status != "published" || !stringSliceEqual(allowedItem.Permissions, []string{"request_access"}) {
		t.Fatalf("allowed team gray item = %#v", allowedItem)
	}

	blockedEngine := buildSkillCatalogEngine(t, 99)
	w = getRequest(blockedEngine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("blocked team status = %d body=%s", w.Code, w.Body.String())
	}
	var blocked struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &blocked); err != nil {
		t.Fatalf("decode blocked team catalog: %v body=%s", err, w.Body.String())
	}
	blockedItem := findSkillCatalogTestItem(blocked.Data.Items, "productShot")
	if blockedItem == nil || blockedItem.Source != "gray" || blockedItem.Status != "unpublished" || len(blockedItem.Permissions) != 0 {
		t.Fatalf("blocked team gray item = %#v", blockedItem)
	}
}

func TestListSkills_AppliesGrayReleasePercentageCohort(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:gray")
	t.Setenv("WORKMAX_WORKAGENT_GRAY_SKILL_PERCENT", "productShot:0")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("blocked percentage status = %d body=%s", w.Code, w.Body.String())
	}
	var blocked struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &blocked); err != nil {
		t.Fatalf("decode blocked percentage catalog: %v body=%s", err, w.Body.String())
	}
	blockedItem := findSkillCatalogTestItem(blocked.Data.Items, "productShot")
	if blockedItem == nil || blockedItem.Status != "unpublished" || len(blockedItem.Permissions) != 0 {
		t.Fatalf("blocked percentage gray item = %#v", blockedItem)
	}

	t.Setenv("WORKMAX_WORKAGENT_GRAY_SKILL_PERCENT", "productShot:100")
	w = getRequest(engine, "/skills")
	if w.Code != http.StatusOK {
		t.Fatalf("allowed percentage status = %d body=%s", w.Code, w.Body.String())
	}
	var allowed struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &allowed); err != nil {
		t.Fatalf("decode allowed percentage catalog: %v body=%s", err, w.Body.String())
	}
	allowedItem := findSkillCatalogTestItem(allowed.Data.Items, "productShot")
	if allowedItem == nil || allowedItem.Status != "published" || !stringSliceEqual(allowedItem.Permissions, []string{"request_access"}) {
		t.Fatalf("allowed percentage gray item = %#v", allowedItem)
	}
}

func TestEnsureSkillRuntimeAccess_RejectsGatedSkillUntilApproved(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:marketplace")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	decision, err := ensureSkillRuntimeAccess(42, "productShot")
	if err != nil {
		t.Fatalf("runtime access check: %v", err)
	}
	if decision.Allowed || decision.Source != "marketplace" || !strings.Contains(decision.Message, "requires approval") {
		t.Fatalf("decision before approval = %#v", decision)
	}
	if err := db.Create(&workagentModel.SkillAccessRequest{
		UID:       42,
		AgentMode: "productShot",
		Source:    "marketplace",
		Status:    workagentModel.SkillAccessRequestStatusApproved,
		Reason:    "approved by manager",
	}).Error; err != nil {
		t.Fatalf("seed approved request: %v", err)
	}
	decision, err = ensureSkillRuntimeAccess(42, "productShot")
	if err != nil {
		t.Fatalf("runtime access check after approval: %v", err)
	}
	if !decision.Allowed || decision.Source != "marketplace" {
		t.Fatalf("decision after approval = %#v", decision)
	}
	otherUserDecision, err := ensureSkillRuntimeAccess(99, "productShot")
	if err != nil {
		t.Fatalf("runtime access check other user: %v", err)
	}
	if otherUserDecision.Allowed {
		t.Fatalf("other user should not inherit approval: %#v", otherUserDecision)
	}
}

func TestEnsureSkillRuntimeAccess_RejectsGraySkillOutsideCohort(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:gray")
	t.Setenv("WORKMAX_WORKAGENT_GRAY_SKILL_USERS", "productShot:42")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	decision, err := ensureSkillRuntimeAccess(99, "productShot")
	if err != nil {
		t.Fatalf("runtime access check: %v", err)
	}
	if decision.Allowed || decision.Source != "gray" || decision.Code != "SKILL_UNAVAILABLE" || !strings.Contains(decision.Message, "gray-release cohort") {
		t.Fatalf("gray non-cohort decision = %#v", decision)
	}
}

func TestEnsureSkillRuntimeAccess_AllowsGraySkillAfterCohortApproval(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:gray")
	t.Setenv("WORKMAX_WORKAGENT_GRAY_SKILL_USERS", "productShot:42")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	if err := db.Create(&workagentModel.SkillAccessRequest{
		UID:       42,
		AgentMode: "productShot",
		Source:    "gray",
		Status:    workagentModel.SkillAccessRequestStatusApproved,
		Reason:    "approved by manager",
	}).Error; err != nil {
		t.Fatalf("seed approved gray request: %v", err)
	}

	decision, err := ensureSkillRuntimeAccess(42, "productShot")
	if err != nil {
		t.Fatalf("runtime access check: %v", err)
	}
	if !decision.Allowed || decision.Source != "gray" {
		t.Fatalf("gray decision after cohort approval = %#v", decision)
	}
}

func TestEnsureSkillRuntimeAccess_RejectsGraySkillOutsideProjectCohort(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:gray")
	t.Setenv("WORKMAX_WORKAGENT_GRAY_SKILL_PROJECTS", "productShot:77")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	decision, err := ensureSkillRuntimeAccessForProject(42, "productShot", 88)
	if err != nil {
		t.Fatalf("runtime project access check: %v", err)
	}
	if decision.Allowed || decision.Source != "gray" || decision.Code != "SKILL_UNAVAILABLE" {
		t.Fatalf("gray non-project decision = %#v", decision)
	}

	decision, err = ensureSkillRuntimeAccessForProject(42, "productShot", 77)
	if err != nil {
		t.Fatalf("runtime allowed project access check: %v", err)
	}
	if decision.Allowed || decision.Source != "gray" || decision.Code == "SKILL_UNAVAILABLE" {
		t.Fatalf("gray allowed project decision should reach approval gate, got %#v", decision)
	}
}

func TestEnsureSkillRuntimeAccess_RejectsGraySkillOutsidePercentageCohort(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:gray")
	t.Setenv("WORKMAX_WORKAGENT_GRAY_SKILL_PERCENT", "productShot:0")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	decision, err := ensureSkillRuntimeAccessForProject(42, "productShot", 0)
	if err != nil {
		t.Fatalf("runtime percentage access check: %v", err)
	}
	if decision.Allowed || decision.Source != "gray" || decision.Code != "SKILL_UNAVAILABLE" {
		t.Fatalf("gray percentage decision = %#v", decision)
	}
}

func TestEnsureSkillRuntimeAccess_RejectsGraySkillOutsideTeamCohort(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:gray")
	t.Setenv("WORKMAX_WORKAGENT_GRAY_SKILL_TEAMS", "productShot:7001")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	seedWorkAgentTeamMember(t, db, 7001, 42)

	decision, err := ensureSkillRuntimeAccessForProject(99, "productShot", 0)
	if err != nil {
		t.Fatalf("runtime team access check: %v", err)
	}
	if decision.Allowed || decision.Source != "gray" || decision.Code != "SKILL_UNAVAILABLE" {
		t.Fatalf("gray non-team decision = %#v", decision)
	}

	decision, err = ensureSkillRuntimeAccessForProject(42, "productShot", 0)
	if err != nil {
		t.Fatalf("runtime allowed team access check: %v", err)
	}
	if decision.Allowed || decision.Source != "gray" || decision.Code == "SKILL_UNAVAILABLE" {
		t.Fatalf("gray allowed team decision should reach approval gate, got %#v", decision)
	}
}

func TestEnsureSkillRuntimeAccess_RejectsPaidSkillWithRequiredTier(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:paid:pro")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	decision, err := ensureSkillRuntimeAccess(42, "productShot")
	if err != nil {
		t.Fatalf("runtime access check: %v", err)
	}
	if decision.Allowed || decision.Source != "paid" || decision.RequiredTier != "pro" || !strings.Contains(decision.Message, "pro approval") {
		t.Fatalf("paid decision before approval = %#v", decision)
	}

	payload := buildSkillAccessDeniedPayload(decision.Message, decision.Source, decision.RequiredTier)
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("decode paid access payload: %v", err)
	}
	if body["type"] != "result" ||
		body["subtype"] != "skill_access_required" ||
		body["code"] != "SKILL_ACCESS_REQUIRED" ||
		body["source"] != "paid" ||
		body["required_tier"] != "pro" ||
		body["is_error"] != true {
		t.Fatalf("paid access payload = %#v", body)
	}
}

func TestEnsureSkillRuntimeAccess_AllowsPaidSkillAfterApproval(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:paid:pro")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	if err := db.Create(&workagentModel.SkillAccessRequest{
		UID:       42,
		AgentMode: "productShot",
		Source:    "paid",
		Status:    workagentModel.SkillAccessRequestStatusApproved,
		Reason:    "approved by manager",
	}).Error; err != nil {
		t.Fatalf("seed approved paid request: %v", err)
	}

	decision, err := ensureSkillRuntimeAccess(42, "productShot")
	if err != nil {
		t.Fatalf("runtime access check: %v", err)
	}
	if !decision.Allowed || decision.Source != "paid" || decision.RequiredTier != "pro" {
		t.Fatalf("paid decision after approval = %#v", decision)
	}
}

func TestEnsureSkillRuntimeAccess_RejectsUnpublishedSkill(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_SKILL_STATUSES", "productShot:unpublished")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)

	decision, err := ensureSkillRuntimeAccess(42, "productShot")
	if err != nil {
		t.Fatalf("runtime access check: %v", err)
	}
	if decision.Allowed || decision.Code != "SKILL_UNAVAILABLE" || !strings.Contains(decision.Message, "unpublished") {
		t.Fatalf("unpublished decision = %#v", decision)
	}
}

func TestEnsureSkillRuntimeAccess_AllowsPaidSkillForPremiumUser(t *testing.T) {
	t.Setenv("WORKMAX_WORKAGENT_GATED_SKILLS", "productShot:paid:pro")
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	seedWorkAgentPremiumUser(t, db, 42)

	decision, err := ensureSkillRuntimeAccess(42, "productShot")
	if err != nil {
		t.Fatalf("runtime access check: %v", err)
	}
	if !decision.Allowed || decision.Source != "paid" || decision.RequiredTier != "pro" {
		t.Fatalf("paid decision for premium user = %#v", decision)
	}
}

func TestEnsureSkillRuntimeAccess_AllowsUngatedSkill(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	decision, err := ensureSkillRuntimeAccess(42, "ppt")
	if err != nil {
		t.Fatalf("runtime access check: %v", err)
	}
	if !decision.Allowed || decision.Source != "official" {
		t.Fatalf("ungated decision = %#v", decision)
	}
}

func TestRequestSkillAccess_RejectsUnknownSkill(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := postJSON(engine, "/skills/not-a-skill/access-requests", map[string]any{"reason": "test"})
	if !strings.Contains(w.Body.String(), "Unknown skill") {
		t.Fatalf("expected unknown skill response, got status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestListSkillAccessRequests_FiltersPendingRequests(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	if err := db.Create(&workagentModel.SkillAccessRequest{
		UID:       42,
		AgentMode: "ppt",
		Source:    "official",
		Status:    workagentModel.SkillAccessRequestStatusPending,
		Reason:    "need access",
	}).Error; err != nil {
		t.Fatalf("seed pending request: %v", err)
	}
	if err := db.Create(&workagentModel.SkillAccessRequest{
		UID:       7,
		AgentMode: "logo",
		Source:    "marketplace",
		Status:    workagentModel.SkillAccessRequestStatusApproved,
		Reason:    "approved",
	}).Error; err != nil {
		t.Fatalf("seed approved request: %v", err)
	}
	engine := buildSkillCatalogEngine(t, 1)

	w := getRequest(engine, "/skills/access-requests?status=pending")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []workagentModel.SkillAccessRequest `json:"items"`
			Count int                                 `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if body.Data.Count != 1 || body.Data.Items[0].AgentMode != "ppt" || body.Data.Items[0].Status != workagentModel.SkillAccessRequestStatusPending {
		t.Fatalf("filtered requests = %#v", body.Data)
	}
}

func TestUpdateSkillAccessRequestStatus_ApprovesRequest(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	request := workagentModel.SkillAccessRequest{
		UID:       42,
		AgentMode: "ppt",
		Source:    "official",
		Status:    workagentModel.SkillAccessRequestStatusPending,
		Reason:    "need access",
	}
	if err := db.Create(&request).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
	engine := buildSkillCatalogEngine(t, 1)

	w := patchJSON(engine, "/skills/access-requests/"+uintToStr(request.Id)+"/status", map[string]any{
		"status":     workagentModel.SkillAccessRequestStatusApproved,
		"reviewNote": "Approved for launch workspace",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var updated workagentModel.SkillAccessRequest
	if err := db.First(&updated, request.Id).Error; err != nil {
		t.Fatalf("reload request: %v", err)
	}
	if updated.Status != workagentModel.SkillAccessRequestStatusApproved {
		t.Fatalf("status = %q, want approved", updated.Status)
	}
	if updated.ReviewedBy != 1 || updated.ReviewedAt == nil || updated.ReviewNote != "Approved for launch workspace" {
		t.Fatalf("review metadata = by:%d at:%v note:%q", updated.ReviewedBy, updated.ReviewedAt, updated.ReviewNote)
	}
	if !strings.Contains(w.Body.String(), `"review_note":"Approved for launch workspace"`) || !strings.Contains(w.Body.String(), `"reviewed_by":1`) {
		t.Fatalf("response missing review metadata: %s", w.Body.String())
	}
}

func TestListDesignSystems_ReturnsCatalog(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/design-systems")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []skills.DesignSystemCatalogItem `json:"items"`
			Count int                              `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Count < 8 || len(body.Data.Items) != body.Data.Count {
		t.Fatalf("count/items = %d/%d, want shipped design systems", body.Data.Count, len(body.Data.Items))
	}
	if body.Data.Items[0].Basename == "" || body.Data.Items[0].Title == "" || body.Data.Items[0].Body == "" {
		t.Fatalf("first design system missing fields: %#v", body.Data.Items[0])
	}
	if body.Data.Items[0].Source != "official" || !body.Data.Items[0].ReadOnly || body.Data.Items[0].Version != "shipped" {
		t.Fatalf("official design system governance metadata = %#v", body.Data.Items[0])
	}
	if !stringSliceEqual(body.Data.Items[0].Permissions, []string{"use", "fork"}) {
		t.Fatalf("official design system permissions = %#v", body.Data.Items[0].Permissions)
	}
}

func TestListDesignSystems_IncludesConfirmedProjectCandidates(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Project design system")
	candidate := workagentModel.ArtifactAssetCandidate{
		UID:          42,
		ThreadID:     thread.Id,
		ArtifactID:   99,
		ThreadFileID: 100,
		AssetKind:    workagentModel.ArtifactAssetKindDesignSystem,
		Name:         "Acme Campaign System",
		Slug:         "acme-campaign-system",
		ProfileJSON:  validDesignSystemCandidateProfileJSON(),
		Status:       workagentModel.ArtifactAssetCandidateStatusConfirmed,
		TargetKind:   workagentModel.ArtifactAssetCandidateTargetDesignSystem,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	if err := db.Create(&workagentModel.ProjectDesignSystem{
		UID:         42,
		ProjectID:   thread.ProjectID,
		ThreadID:    thread.Id,
		ArtifactID:  candidate.ArtifactID,
		CandidateID: candidate.Id,
		Name:        "Acme Campaign System",
		Slug:        "acme-campaign-system",
		Basename:    "project-acme-campaign-system",
		Title:       "Acme Campaign System",
		DerivedFrom: "artifact-99",
		Body:        validDesignSystemMarkdown(),
		Status:      workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}).Error; err != nil {
		t.Fatalf("seed materialized project design system: %v", err)
	}
	if err := db.Create(&workagentModel.ArtifactAssetCandidate{
		UID:        99,
		ThreadID:   thread.Id,
		ArtifactID: 100,
		AssetKind:  workagentModel.ArtifactAssetKindDesignSystem,
		Name:       "Cross Tenant System",
		Status:     workagentModel.ArtifactAssetCandidateStatusConfirmed,
		TargetKind: workagentModel.ArtifactAssetCandidateTargetDesignSystem,
	}).Error; err != nil {
		t.Fatalf("seed cross tenant candidate: %v", err)
	}

	engine := buildSkillCatalogEngine(t, 42)
	w := getRequest(engine, "/design-systems?projectId=77")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []skills.DesignSystemCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, item := range body.Data.Items {
		if item.Title == "Cross Tenant System" {
			t.Fatalf("cross-tenant project design system leaked: %#v", item)
		}
		if item.Title != "Acme Campaign System" {
			continue
		}
		found = true
		if !strings.Contains(item.Basename, "acme-campaign-system") {
			t.Fatalf("basename = %q", item.Basename)
		}
		if item.DerivedFrom != "artifact-99" {
			t.Fatalf("derivedFrom = %q", item.DerivedFrom)
		}
		if !strings.Contains(item.Body, "## 1. Color") {
			t.Fatalf("candidate body = %s", item.Body)
		}
		if item.Source != "project" || item.ProjectID != thread.ProjectID || item.CandidateID != candidate.Id || item.ReadOnly {
			t.Fatalf("project governance metadata = %#v", item)
		}
		if !stringSliceEqual(item.Permissions, []string{"use", "fork", "archive"}) {
			t.Fatalf("project design system permissions = %#v", item.Permissions)
		}
	}
	if !found {
		t.Fatalf("project design system candidate missing from catalog")
	}
}

func TestListDesignSystems_UsesProjectMemberRolePermissions(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Project member design system")
	seedWorkAgentProjectOwner(t, db, thread.ProjectID, 42)
	seedWorkAgentProjectMember(t, db, thread.ProjectID, 7, model.GlobalProjectRoleEditor)
	projectSystem := workagentModel.ProjectDesignSystem{
		UID:       42,
		ProjectID: thread.ProjectID,
		ThreadID:  thread.Id,
		Name:      "Team Campaign System",
		Slug:      "team-campaign-system",
		Basename:  "project-team-campaign-system",
		Title:     "Team Campaign System",
		Body:      validDesignSystemMarkdown(),
		Status:    workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	if err := db.Create(&projectSystem).Error; err != nil {
		t.Fatalf("seed project design system: %v", err)
	}

	engine := buildSkillCatalogEngine(t, 7)
	w := getRequest(engine, "/design-systems?projectId="+uintToStr(thread.ProjectID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []skills.DesignSystemCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	for _, item := range body.Data.Items {
		if item.DesignSystemID != projectSystem.Id {
			continue
		}
		if !stringSliceEqual(item.Permissions, []string{"use", "fork"}) {
			t.Fatalf("editor design system permissions = %#v", item.Permissions)
		}
		return
	}
	t.Fatalf("project member did not see project design system: %#v", body.Data.Items)
}

func TestListDesignSystems_IncludePendingProjectSystemsForApproval(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Pending project design system")
	projectSystem := workagentModel.ProjectDesignSystem{
		UID:         42,
		ProjectID:   thread.ProjectID,
		ThreadID:    thread.Id,
		ArtifactID:  101,
		Name:        "Pending Campaign System",
		Slug:        "pending-campaign-system",
		Basename:    "project-pending-campaign-system",
		Title:       "Pending Campaign System",
		DerivedFrom: "artifact-101",
		Body:        validDesignSystemMarkdown(),
		Status:      workagentModel.ArtifactAssetCandidateStatusDraft,
	}
	if err := db.Create(&projectSystem).Error; err != nil {
		t.Fatalf("seed project design system: %v", err)
	}
	engine := buildSkillCatalogEngine(t, 42)
	w := getRequest(engine, "/design-systems?projectId=77")
	if strings.Contains(w.Body.String(), "Pending Campaign System") {
		t.Fatalf("pending project design system should not appear by default: %s", w.Body.String())
	}
	w = getRequest(engine, "/design-systems?projectId=77&includePending=true")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []skills.DesignSystemCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, item := range body.Data.Items {
		if item.Title != "Pending Campaign System" {
			continue
		}
		if item.Status != workagentModel.ArtifactAssetCandidateStatusDraft || item.Source != "project" || item.DesignSystemID != projectSystem.Id {
			t.Fatalf("pending design system metadata = %#v", item)
		}
		if !stringSliceEqual(item.Permissions, []string{"confirm", "reject", "archive"}) {
			t.Fatalf("pending design system permissions = %#v", item.Permissions)
		}
		return
	}
	t.Fatalf("pending project design system missing from includePending catalog: %#v", body.Data.Items)
}

func TestUpdateProjectDesignSystemStatus_ArchivesProjectSystemWithoutCandidateFallback(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Project design system")
	candidate := workagentModel.ArtifactAssetCandidate{
		UID:          42,
		ThreadID:     thread.Id,
		ArtifactID:   99,
		ThreadFileID: 100,
		AssetKind:    workagentModel.ArtifactAssetKindDesignSystem,
		Name:         "Archived Campaign System",
		Slug:         "archived-campaign-system",
		ProfileJSON:  validDesignSystemCandidateProfileJSON(),
		Status:       workagentModel.ArtifactAssetCandidateStatusConfirmed,
		TargetKind:   workagentModel.ArtifactAssetCandidateTargetDesignSystem,
	}
	if err := db.Create(&candidate).Error; err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	projectSystem := workagentModel.ProjectDesignSystem{
		UID:         42,
		ProjectID:   thread.ProjectID,
		ThreadID:    thread.Id,
		ArtifactID:  candidate.ArtifactID,
		CandidateID: candidate.Id,
		Name:        "Archived Campaign System",
		Slug:        "archived-campaign-system",
		Basename:    "project-archived-campaign-system",
		Title:       "Archived Campaign System",
		DerivedFrom: "artifact-99",
		Version:     1,
		Body:        validDesignSystemMarkdown(),
		Status:      workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	if err := db.Create(&projectSystem).Error; err != nil {
		t.Fatalf("seed project design system: %v", err)
	}

	engine := buildSkillCatalogEngine(t, 42)
	w := patchJSON(engine, "/projects/"+uintToStr(thread.ProjectID)+"/design-systems/"+uintToStr(projectSystem.Id)+"/status", map[string]any{
		"status":     workagentModel.ProjectDesignSystemStatusArchived,
		"reviewNote": "Archived after brand refresh",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("archive status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"archived"`) {
		t.Fatalf("archive response should carry archived status, got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"review_note":"Archived after brand refresh"`) || !strings.Contains(w.Body.String(), `"reviewed_by":42`) {
		t.Fatalf("archive response should carry review metadata, got %s", w.Body.String())
	}
	var reloaded workagentModel.ProjectDesignSystem
	if err := db.First(&reloaded, projectSystem.Id).Error; err != nil {
		t.Fatalf("reload project design system: %v", err)
	}
	if reloaded.ReviewedBy != 42 || reloaded.ReviewedAt == nil || reloaded.ReviewNote != "Archived after brand refresh" {
		t.Fatalf("review metadata = by:%d at:%v note:%q", reloaded.ReviewedBy, reloaded.ReviewedAt, reloaded.ReviewNote)
	}

	w = getRequest(engine, "/design-systems?projectId="+uintToStr(thread.ProjectID))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "Archived Campaign System") {
		t.Fatalf("archived project design system leaked back through catalog/candidate fallback: %s", w.Body.String())
	}
}

func TestUpdateProjectDesignSystemStatus_RejectsProjectEditorApproval(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Editor design system approval")
	seedWorkAgentProjectOwner(t, db, thread.ProjectID, 42)
	seedWorkAgentProjectMember(t, db, thread.ProjectID, 7, model.GlobalProjectRoleEditor)
	projectSystem := workagentModel.ProjectDesignSystem{
		UID:       42,
		ProjectID: thread.ProjectID,
		ThreadID:  thread.Id,
		Name:      "Editor Cannot Archive",
		Slug:      "editor-cannot-archive",
		Basename:  "project-editor-cannot-archive",
		Title:     "Editor Cannot Archive",
		Body:      validDesignSystemMarkdown(),
		Status:    workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	if err := db.Create(&projectSystem).Error; err != nil {
		t.Fatalf("seed project design system: %v", err)
	}

	engine := buildSkillCatalogEngine(t, 7)
	w := patchJSON(engine, "/projects/"+uintToStr(thread.ProjectID)+"/design-systems/"+uintToStr(projectSystem.Id)+"/status", map[string]any{
		"status":     workagentModel.ProjectDesignSystemStatusArchived,
		"reviewNote": "editor attempted archive",
	})
	if !strings.Contains(w.Body.String(), "load project design system") {
		t.Fatalf("editor archive should fail generically, got status=%d body=%s", w.Code, w.Body.String())
	}
	var reloaded workagentModel.ProjectDesignSystem
	if err := db.First(&reloaded, projectSystem.Id).Error; err != nil {
		t.Fatalf("reload project system: %v", err)
	}
	if reloaded.Status != workagentModel.ArtifactAssetCandidateStatusConfirmed || reloaded.ReviewedBy != 0 {
		t.Fatalf("editor archive mutated project system: status=%q reviewedBy=%d", reloaded.Status, reloaded.ReviewedBy)
	}
}

func TestUpdateProjectDesignSystemStatus_RejectsPendingProjectSystem(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Reject pending project design system")
	projectSystem := workagentModel.ProjectDesignSystem{
		UID:         42,
		ProjectID:   thread.ProjectID,
		ThreadID:    thread.Id,
		ArtifactID:  121,
		Name:        "Rejected Campaign System",
		Slug:        "rejected-campaign-system",
		Basename:    "project-rejected-campaign-system",
		Title:       "Rejected Campaign System",
		DerivedFrom: "artifact-121",
		Body:        validDesignSystemMarkdown(),
		Status:      workagentModel.ArtifactAssetCandidateStatusDraft,
	}
	if err := db.Create(&projectSystem).Error; err != nil {
		t.Fatalf("seed project design system: %v", err)
	}

	engine := buildSkillCatalogEngine(t, 42)
	w := patchJSON(engine, "/projects/"+uintToStr(thread.ProjectID)+"/design-systems/"+uintToStr(projectSystem.Id)+"/status", map[string]any{
		"status":     workagentModel.ProjectDesignSystemStatusRejected,
		"reviewNote": "Rejected from Project Assets",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("reject status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"status":"rejected"`) {
		t.Fatalf("reject response should carry rejected status, got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"review_note":"Rejected from Project Assets"`) || !strings.Contains(w.Body.String(), `"reviewed_by":42`) {
		t.Fatalf("reject response should carry review metadata, got %s", w.Body.String())
	}

	w = getRequest(engine, "/design-systems?projectId="+uintToStr(thread.ProjectID))
	if strings.Contains(w.Body.String(), "Rejected Campaign System") {
		t.Fatalf("rejected project design system should not appear by default: %s", w.Body.String())
	}
	w = getRequest(engine, "/design-systems?projectId="+uintToStr(thread.ProjectID)+"&includePending=true")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []skills.DesignSystemCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	for _, item := range body.Data.Items {
		if item.DesignSystemID != projectSystem.Id {
			continue
		}
		if item.Status != workagentModel.ProjectDesignSystemStatusRejected || item.Source != "project" {
			t.Fatalf("rejected design system metadata = %#v", item)
		}
		if !stringSliceEqual(item.Permissions, []string{"archive"}) {
			t.Fatalf("rejected design system permissions = %#v", item.Permissions)
		}
		return
	}
	t.Fatalf("rejected project design system missing from includePending catalog: %#v", body.Data.Items)
}

func TestUpdateProjectDesignSystemStatus_RejectsInvalidDraftOnConfirm(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Invalid design system")
	projectSystem := workagentModel.ProjectDesignSystem{
		UID:         42,
		ProjectID:   thread.ProjectID,
		ThreadID:    thread.Id,
		ArtifactID:  120,
		Name:        "Invalid Campaign System",
		Slug:        "invalid-campaign-system",
		Basename:    "project-invalid-campaign-system",
		Title:       "Invalid Campaign System",
		DerivedFrom: "artifact-120",
		Body:        "# Invalid Campaign System\n\n## 1. Color\nOnly colors, missing the rest.",
		Status:      workagentModel.ArtifactAssetCandidateStatusDraft,
	}
	if err := db.Create(&projectSystem).Error; err != nil {
		t.Fatalf("seed project design system: %v", err)
	}
	engine := buildSkillCatalogEngine(t, 42)
	w := patchJSON(engine, "/projects/77/design-systems/"+uintToStr(projectSystem.Id)+"/status", map[string]any{
		"status": workagentModel.ArtifactAssetCandidateStatusConfirmed,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("response wrapper should stay HTTP 200, got %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "validate project design system before confirm") {
		t.Fatalf("confirm failure should mention validation, got %s", w.Body.String())
	}
	var reloaded workagentModel.ProjectDesignSystem
	if err := db.First(&reloaded, projectSystem.Id).Error; err != nil {
		t.Fatalf("reload project system: %v", err)
	}
	if reloaded.Status != workagentModel.ArtifactAssetCandidateStatusDraft {
		t.Fatalf("invalid project design system status = %q, want draft", reloaded.Status)
	}
}

func TestUpdateProjectDesignSystemStatus_RejectsCrossTenantProjectSystem(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 99, "Other project design system")
	projectSystem := workagentModel.ProjectDesignSystem{
		UID:       99,
		ProjectID: thread.ProjectID,
		ThreadID:  thread.Id,
		Name:      "Other System",
		Basename:  "other-system",
		Title:     "Other System",
		Body:      validDesignSystemMarkdown(),
		Status:    workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	if err := db.Create(&projectSystem).Error; err != nil {
		t.Fatalf("seed project design system: %v", err)
	}

	engine := buildSkillCatalogEngine(t, 42)
	w := patchJSON(engine, "/projects/"+uintToStr(thread.ProjectID)+"/design-systems/"+uintToStr(projectSystem.Id)+"/status", map[string]any{
		"status": workagentModel.ProjectDesignSystemStatusArchived,
	})
	if !strings.Contains(w.Body.String(), "load project design system") {
		t.Fatalf("cross-tenant archive should fail generically, got status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestForkProjectDesignSystem_CreatesDerivedProjectSystem(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Fork project design system")
	sourceCandidate := workagentModel.ArtifactAssetCandidate{
		UID:          42,
		ThreadID:     thread.Id,
		ArtifactID:   99,
		ThreadFileID: 100,
		AssetKind:    workagentModel.ArtifactAssetKindDesignSystem,
		Name:         "Acme Campaign System",
		Slug:         "acme-campaign-system",
		ProfileJSON:  validDesignSystemCandidateProfileJSON(),
		Status:       workagentModel.ArtifactAssetCandidateStatusConfirmed,
		TargetKind:   workagentModel.ArtifactAssetCandidateTargetDesignSystem,
	}
	if err := db.Create(&sourceCandidate).Error; err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	source := workagentModel.ProjectDesignSystem{
		UID:         42,
		ProjectID:   thread.ProjectID,
		ThreadID:    thread.Id,
		ArtifactID:  sourceCandidate.ArtifactID,
		CandidateID: sourceCandidate.Id,
		Name:        "Acme Campaign System",
		Slug:        "acme-campaign-system",
		Basename:    "project-acme-campaign-system",
		Title:       "Acme Campaign System",
		DerivedFrom: "artifact-99",
		Body:        validDesignSystemMarkdown(),
		Status:      workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("seed source design system: %v", err)
	}

	engine := buildSkillCatalogEngine(t, 42)
	w := postJSON(engine, "/projects/"+uintToStr(thread.ProjectID)+"/design-systems/"+uintToStr(source.Id)+"/fork", map[string]any{
		"name": "Acme Campaign System v2",
		"slug": "acme-campaign-v2",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("fork status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			ProjectID            uint   `json:"project_id"`
			SourceDesignSystemID uint   `json:"source_design_system_id"`
			DesignSystemID       uint   `json:"design_system_id"`
			CandidateID          uint   `json:"candidate_id"`
			Basename             string `json:"basename"`
			Title                string `json:"title"`
			DerivedFrom          string `json:"derived_from"`
			Version              int    `json:"version"`
			Status               string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode fork response: %v", err)
	}
	if body.Data.ProjectID != thread.ProjectID || body.Data.SourceDesignSystemID != source.Id {
		t.Fatalf("fork trace fields = %#v", body.Data)
	}
	if body.Data.DesignSystemID == 0 || body.Data.CandidateID == 0 {
		t.Fatalf("fork should create design system and candidate ids: %#v", body.Data)
	}
	if body.Data.Title != "Acme Campaign System v2" || body.Data.DerivedFrom != source.Basename || body.Data.Status != workagentModel.ArtifactAssetCandidateStatusConfirmed {
		t.Fatalf("fork response fields = %#v", body.Data)
	}
	if body.Data.Version != 2 {
		t.Fatalf("fork version = %d, want 2", body.Data.Version)
	}
	if !strings.Contains(body.Data.Basename, "acme-campaign-v2") {
		t.Fatalf("fork basename should carry requested slug, got %q", body.Data.Basename)
	}

	var candidate workagentModel.ArtifactAssetCandidate
	if err := db.Where("id = ?", body.Data.CandidateID).First(&candidate).Error; err != nil {
		t.Fatalf("load fork candidate: %v", err)
	}
	if candidate.TargetKind != workagentModel.ArtifactAssetCandidateTargetDesignSystem || candidate.TargetID != body.Data.DesignSystemID {
		t.Fatalf("fork candidate target = %#v targetID=%d", candidate.TargetKind, candidate.TargetID)
	}
	if !strings.Contains(candidate.ProfileJSON, `"forkedFrom":"project-acme-campaign-system"`) {
		t.Fatalf("fork candidate profile should keep source trace, got %s", candidate.ProfileJSON)
	}

	w = getRequest(engine, "/design-systems?projectId="+uintToStr(thread.ProjectID))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Acme Campaign System") || !strings.Contains(w.Body.String(), "Acme Campaign System v2") {
		t.Fatalf("catalog should include both source and fork, got %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"version":"v2"`) {
		t.Fatalf("catalog should expose fork version v2, got %s", w.Body.String())
	}
}

func TestForkProjectDesignSystem_AllowsProjectEditor(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Editor fork project design system")
	seedWorkAgentProjectOwner(t, db, thread.ProjectID, 42)
	seedWorkAgentProjectMember(t, db, thread.ProjectID, 7, model.GlobalProjectRoleEditor)
	source := workagentModel.ProjectDesignSystem{
		UID:       42,
		ProjectID: thread.ProjectID,
		ThreadID:  thread.Id,
		Name:      "Shared System",
		Slug:      "shared-system",
		Basename:  "project-shared-system",
		Title:     "Shared System",
		Body:      validDesignSystemMarkdown(),
		Status:    workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("seed source design system: %v", err)
	}

	engine := buildSkillCatalogEngine(t, 7)
	w := postJSON(engine, "/projects/"+uintToStr(thread.ProjectID)+"/design-systems/"+uintToStr(source.Id)+"/fork", map[string]any{
		"name": "Editor Forked System",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("editor fork status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			DesignSystemID uint `json:"design_system_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode fork response: %v body=%s", err, w.Body.String())
	}
	var fork workagentModel.ProjectDesignSystem
	if err := db.First(&fork, body.Data.DesignSystemID).Error; err != nil {
		t.Fatalf("load editor fork: %v", err)
	}
	if fork.UID != 7 || fork.ProjectID != thread.ProjectID || fork.DerivedFrom != source.Basename {
		t.Fatalf("editor fork = %#v", fork)
	}
}

func TestForkProjectDesignSystem_RejectsCrossTenantProjectSystem(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 99, "Other fork project design system")
	source := workagentModel.ProjectDesignSystem{
		UID:       99,
		ProjectID: thread.ProjectID,
		ThreadID:  thread.Id,
		Name:      "Other System",
		Basename:  "other-system",
		Title:     "Other System",
		Body:      validDesignSystemMarkdown(),
		Status:    workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("seed source design system: %v", err)
	}

	engine := buildSkillCatalogEngine(t, 42)
	w := postJSON(engine, "/projects/"+uintToStr(thread.ProjectID)+"/design-systems/"+uintToStr(source.Id)+"/fork", map[string]any{
		"name": "Should not fork",
	})
	if !strings.Contains(w.Body.String(), "load project design system") {
		t.Fatalf("cross-tenant fork should fail generically, got status=%d body=%s", w.Code, w.Body.String())
	}
}

type designSystemHistoryAPIItem struct {
	DesignSystemID uint   `json:"designSystemId"`
	Basename       string `json:"basename"`
	DerivedFrom    string `json:"derivedFrom"`
	Version        string `json:"version"`
	VersionDiff    string `json:"versionDiff"`
	Status         string `json:"status"`
	Source         string `json:"source"`
	ReviewedBy     int    `json:"reviewedBy"`
	ReviewNote     string `json:"reviewNote"`
}

func TestGetProjectDesignSystemHistory_ReturnsLineageAndBranches(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Design system history")
	source := workagentModel.ProjectDesignSystem{
		UID:         42,
		ProjectID:   thread.ProjectID,
		ThreadID:    thread.Id,
		Name:        "Source System",
		Slug:        "source-system",
		Basename:    "source-system",
		Title:       "Source System",
		DerivedFrom: "modern-minimal",
		Version:     1,
		Body:        validDesignSystemMarkdown(),
		Status:      workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	fork := workagentModel.ProjectDesignSystem{
		UID:         42,
		ProjectID:   thread.ProjectID,
		ThreadID:    thread.Id,
		Name:        "Fork System",
		Slug:        "fork-system",
		Basename:    "fork-system",
		Title:       "Fork System",
		DerivedFrom: "source-system",
		Version:     2,
		Body:        strings.Replace(validDesignSystemMarkdown(), "#3151c4 | accent |", "#d946ef | magenta accent |", 1),
		Status:      workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	branch := workagentModel.ProjectDesignSystem{
		UID:         42,
		ProjectID:   thread.ProjectID,
		ThreadID:    thread.Id,
		Name:        "Branch System",
		Slug:        "branch-system",
		Basename:    "branch-system",
		Title:       "Branch System",
		DerivedFrom: "source-system",
		Version:     2,
		Body:        validDesignSystemMarkdown(),
		Status:      workagentModel.ProjectDesignSystemStatusArchived,
		ReviewedBy:  42,
		ReviewNote:  "Archived stale branch",
	}
	unrelated := workagentModel.ProjectDesignSystem{
		UID:       42,
		ProjectID: thread.ProjectID,
		Name:      "Unrelated",
		Slug:      "unrelated",
		Basename:  "unrelated",
		Title:     "Unrelated",
		Version:   1,
		Body:      validDesignSystemMarkdown(),
		Status:    workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("seed source design system: %v", err)
	}
	if err := db.Create(&fork).Error; err != nil {
		t.Fatalf("seed fork design system: %v", err)
	}
	if err := db.Create(&branch).Error; err != nil {
		t.Fatalf("seed branch design system: %v", err)
	}
	if err := db.Create(&unrelated).Error; err != nil {
		t.Fatalf("seed unrelated design system: %v", err)
	}
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/projects/"+uintToStr(thread.ProjectID)+"/design-systems/"+uintToStr(fork.Id)+"/history")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Items []designSystemHistoryAPIItem `json:"items"`
			Count int                          `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	if body.Data.Count != 4 {
		t.Fatalf("count = %d items=%#v, want official + source + fork + branch", body.Data.Count, body.Data.Items)
	}
	if body.Data.Items[0].Source != "official" || body.Data.Items[0].Basename != "modern-minimal" {
		t.Fatalf("first history item = %#v, want official modern-minimal root", body.Data.Items[0])
	}
	if !historyContains(body.Data.Items, source.Id, "source-system", "v1", workagentModel.ArtifactAssetCandidateStatusConfirmed) {
		t.Fatalf("history missing source row: %#v", body.Data.Items)
	}
	if !historyContains(body.Data.Items, fork.Id, "fork-system", "v2", workagentModel.ArtifactAssetCandidateStatusConfirmed) {
		t.Fatalf("history missing fork row: %#v", body.Data.Items)
	}
	if !historyDiffContains(body.Data.Items, fork.Id, "changed: Color") {
		t.Fatalf("history missing fork version diff: %#v", body.Data.Items)
	}
	if !historyContains(body.Data.Items, branch.Id, "branch-system", "v2", workagentModel.ProjectDesignSystemStatusArchived) {
		t.Fatalf("history missing archived branch row: %#v", body.Data.Items)
	}
	if !historyReviewContains(body.Data.Items, branch.Id, 42, "Archived stale branch") {
		t.Fatalf("history missing branch review metadata: %#v", body.Data.Items)
	}
	if historyContains(body.Data.Items, unrelated.Id, "unrelated", "v1", workagentModel.ArtifactAssetCandidateStatusConfirmed) {
		t.Fatalf("history leaked unrelated row: %#v", body.Data.Items)
	}
}

func TestGetProjectDesignSystemHistory_RejectsCrossTenantProjectSystem(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 99, "Other design system history")
	projectSystem := workagentModel.ProjectDesignSystem{
		UID:       99,
		ProjectID: thread.ProjectID,
		Name:      "Other System",
		Slug:      "other-system",
		Basename:  "other-system",
		Title:     "Other System",
		Version:   1,
		Body:      validDesignSystemMarkdown(),
		Status:    workagentModel.ArtifactAssetCandidateStatusConfirmed,
	}
	if err := db.Create(&projectSystem).Error; err != nil {
		t.Fatalf("seed project design system: %v", err)
	}
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/projects/"+uintToStr(thread.ProjectID)+"/design-systems/"+uintToStr(projectSystem.Id)+"/history")
	if !strings.Contains(w.Body.String(), "load project design system") {
		t.Fatalf("expected ownership failure, got %s", w.Body.String())
	}
}

func historyContains(items []designSystemHistoryAPIItem, id uint, basename string, version string, status string) bool {
	for _, item := range items {
		if item.DesignSystemID == id && item.Basename == basename && item.Version == version && item.Status == status {
			return true
		}
	}
	return false
}

func historyDiffContains(items []designSystemHistoryAPIItem, id uint, fragment string) bool {
	for _, item := range items {
		if item.DesignSystemID == id && strings.Contains(item.VersionDiff, fragment) {
			return true
		}
	}
	return false
}

func historyReviewContains(items []designSystemHistoryAPIItem, id uint, reviewedBy int, reviewNote string) bool {
	for _, item := range items {
		if item.DesignSystemID == id && item.ReviewedBy == reviewedBy && item.ReviewNote == reviewNote {
			return true
		}
	}
	return false
}

func TestForkOfficialDesignSystem_CreatesProjectSystemFromCatalog(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Fork official design system")
	seedWorkAgentProjectOwner(t, db, thread.ProjectID, 42)

	engine := buildSkillCatalogEngine(t, 42)
	w := postJSON(engine, "/projects/"+uintToStr(thread.ProjectID)+"/design-systems/fork", map[string]any{
		"basename": "modern-minimal",
		"name":     "Modern Minimal Project Fork",
		"slug":     "modern-minimal-project",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("fork status = %d body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			ProjectID            uint   `json:"project_id"`
			SourceDesignSystemID string `json:"source_design_system_id"`
			DesignSystemID       uint   `json:"design_system_id"`
			CandidateID          uint   `json:"candidate_id"`
			Basename             string `json:"basename"`
			Title                string `json:"title"`
			DerivedFrom          string `json:"derived_from"`
			Version              int    `json:"version"`
			Status               string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode fork response: %v", err)
	}
	if body.Data.ProjectID != thread.ProjectID || body.Data.SourceDesignSystemID != "modern-minimal" {
		t.Fatalf("fork trace fields = %#v", body.Data)
	}
	if body.Data.DesignSystemID == 0 || body.Data.CandidateID == 0 {
		t.Fatalf("fork should create design system and candidate ids: %#v", body.Data)
	}
	if body.Data.Title != "Modern Minimal Project Fork" || body.Data.DerivedFrom != "modern-minimal" || body.Data.Status != workagentModel.ArtifactAssetCandidateStatusConfirmed {
		t.Fatalf("fork response fields = %#v", body.Data)
	}
	if body.Data.Version != 1 {
		t.Fatalf("official fork version = %d, want 1", body.Data.Version)
	}
	if !strings.Contains(body.Data.Basename, "modern-minimal-project") {
		t.Fatalf("fork basename should carry requested slug, got %q", body.Data.Basename)
	}

	var candidate workagentModel.ArtifactAssetCandidate
	if err := db.Where("id = ?", body.Data.CandidateID).First(&candidate).Error; err != nil {
		t.Fatalf("load fork candidate: %v", err)
	}
	if candidate.ThreadID != 0 || candidate.ArtifactID != 0 {
		t.Fatalf("official fork candidate should not claim artifact/thread provenance: %#v", candidate)
	}
	if candidate.TargetKind != workagentModel.ArtifactAssetCandidateTargetDesignSystem || candidate.TargetID != body.Data.DesignSystemID {
		t.Fatalf("fork candidate target = %#v targetID=%d", candidate.TargetKind, candidate.TargetID)
	}
	if !strings.Contains(candidate.ProfileJSON, `"forkedFrom":"modern-minimal"`) || !strings.Contains(candidate.ProfileJSON, `"forkedFromSource":"official"`) {
		t.Fatalf("fork candidate profile should keep official source trace, got %s", candidate.ProfileJSON)
	}

	w = getRequest(engine, "/design-systems?projectId="+uintToStr(thread.ProjectID))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Modern Minimal Project Fork") {
		t.Fatalf("catalog should include official fork, got %s", w.Body.String())
	}
}

func TestForkOfficialDesignSystem_RejectsMissingCatalogBasename(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	thread := seedConversationThread(t, db, 42, "Missing official design system")
	seedWorkAgentProjectOwner(t, db, thread.ProjectID, 42)

	engine := buildSkillCatalogEngine(t, 42)
	w := postJSON(engine, "/projects/"+uintToStr(thread.ProjectID)+"/design-systems/fork", map[string]any{
		"basename": "not-a-real-design-system",
	})
	if !strings.Contains(w.Body.String(), "official system not found") {
		t.Fatalf("missing official fork should fail, got status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestForkOfficialDesignSystem_RejectsCrossTenantProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	otherThread := seedConversationThread(t, db, 99, "Other official fork project")
	seedWorkAgentProjectOwner(t, db, otherThread.ProjectID, 99)

	engine := buildSkillCatalogEngine(t, 42)
	w := postJSON(engine, "/projects/"+uintToStr(otherThread.ProjectID)+"/design-systems/fork", map[string]any{
		"basename": "modern-minimal",
	})
	if !strings.Contains(w.Body.String(), "load project") {
		t.Fatalf("cross-tenant official fork should fail generically, got status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestListSkills_AlphabeticalOrdering(t *testing.T) {
	// FE picker stability depends on deterministic order. Go map
	// iteration is randomised — the handler must sort.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/skills")
	var body struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	names := make([]string, 0, len(body.Data.Items))
	for _, it := range body.Data.Items {
		names = append(names, it.AgentMode)
	}
	sorted := append([]string{}, names...)
	sort.Strings(sorted)
	for i := range names {
		if names[i] != sorted[i] {
			t.Errorf("position %d: got %q, want %q (catalog must be alphabetical)", i, names[i], sorted[i])
		}
	}
}

func TestListSkills_PPTMetadataShape(t *testing.T) {
	// Pin the load-bearing case: ppt today carries v2.0.0, has
	// question form, has directions fallback, has post-script
	// (validate-pptx.py). If this test ever flips red the catalog
	// has stopped reading the bundle correctly.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/skills")
	var body struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var ppt *skillCatalogItem
	for i := range body.Data.Items {
		if body.Data.Items[i].AgentMode == "ppt" {
			ppt = &body.Data.Items[i]
			break
		}
	}
	if ppt == nil {
		t.Fatalf("ppt missing from catalog")
	}
	if ppt.Version == "" || ppt.Version == "unknown" || ppt.Version == "legacy" {
		t.Errorf("ppt.version = %q, want real semver", ppt.Version)
	}
	if !ppt.HasQuestionForm {
		t.Error("ppt should carry HasQuestionForm=true")
	}
	if !ppt.HasDirectionsFallback {
		t.Error("ppt should carry HasDirectionsFallback=true")
	}
	if !ppt.HasPostScripts {
		t.Error("ppt should carry HasPostScripts=true (validate-pptx.py)")
	}
	if ppt.Source != "official" || ppt.Status != "published" || !stringSliceEqual(ppt.Permissions, []string{"use"}) {
		t.Errorf("ppt governance metadata = source:%q status:%q permissions:%#v", ppt.Source, ppt.Status, ppt.Permissions)
	}
	if len(ppt.RequiredInputs) == 0 || ppt.RequiredInputs[0].Kind == "" {
		t.Errorf("ppt required inputs missing: %#v", ppt.RequiredInputs)
	}
	if !containsString(ppt.RiskHints, "post_generation_validation") {
		t.Errorf("ppt risk hints = %v, want post_generation_validation", ppt.RiskHints)
	}
	if !containsString(ppt.RiskHints, "deck_export_review") {
		t.Errorf("ppt risk hints = %v, want deck_export_review", ppt.RiskHints)
	}
	if !containsString(ppt.RiskHints, "document_export_review") {
		t.Errorf("ppt risk hints = %v, want document_export_review", ppt.RiskHints)
	}
	if ppt.Artifacts == nil {
		t.Fatal("ppt should carry artifact metadata")
	}
	if ppt.Artifacts.PrimaryType != "deck" {
		t.Errorf("ppt artifact primary type = %q, want deck", ppt.Artifacts.PrimaryType)
	}
	if !containsString(ppt.Artifacts.OutputTypes, "pptx") {
		t.Errorf("ppt artifact output types = %v, want pptx", ppt.Artifacts.OutputTypes)
	}
	if !containsString(ppt.Artifacts.PreviewTypes, "deck") {
		t.Errorf("ppt artifact preview types = %v, want deck", ppt.Artifacts.PreviewTypes)
	}
}

func TestListSkills_AllItemsCarryArtifactMetadata(t *testing.T) {
	// P0 design-workspace matrix: every user-facing skill must
	// declare what it produces so P1 artifact registry / preview
	// routing doesn't infer product shape from prose.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/skills")
	var body struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, it := range body.Data.Items {
		if it.Artifacts == nil {
			t.Errorf("skill %q missing artifact metadata", it.AgentMode)
			continue
		}
		if it.Artifacts.PrimaryType == "" {
			t.Errorf("skill %q artifact primary type is empty", it.AgentMode)
		}
		if len(it.Artifacts.OutputTypes) == 0 {
			t.Errorf("skill %q artifact output types are empty", it.AgentMode)
		}
		if len(it.Artifacts.PreviewTypes) == 0 {
			t.Errorf("skill %q artifact preview types are empty", it.AgentMode)
		}
		if len(it.RequiredInputs) == 0 {
			t.Errorf("skill %q missing required input metadata", it.AgentMode)
		}
		if len(it.RiskHints) == 0 {
			t.Errorf("skill %q missing risk hints", it.AgentMode)
		}
	}
}

func TestDeriveSkillRiskHints_UsesExportTargets(t *testing.T) {
	hints := deriveSkillRiskHints(&skills.SkillBundle{
		Artifacts: &skills.ArtifactMetadata{
			OutputTypes:   []string{"markdown"},
			ExportTargets: []string{"html", "gif", "png", "pdf", "pptx"},
		},
	})
	if !containsString(hints, "html_static_validation") {
		t.Errorf("risk hints = %v, want html_static_validation from export target", hints)
	}
	if !containsString(hints, "motion_export_review") {
		t.Errorf("risk hints = %v, want motion_export_review from export target", hints)
	}
	if !containsString(hints, "visual_fidelity_review") {
		t.Errorf("risk hints = %v, want visual_fidelity_review from export target", hints)
	}
	if !containsString(hints, "document_export_review") {
		t.Errorf("risk hints = %v, want document_export_review from export target", hints)
	}
	if !containsString(hints, "deck_export_review") {
		t.Errorf("risk hints = %v, want deck_export_review from export target", hints)
	}
}

func TestDeriveSkillRiskHints_UsesOutputTypesForDeckAndPDF(t *testing.T) {
	hints := deriveSkillRiskHints(&skills.SkillBundle{
		Artifacts: &skills.ArtifactMetadata{
			OutputTypes: []string{"deck", "pdf"},
		},
	})
	if !containsString(hints, "deck_export_review") {
		t.Errorf("risk hints = %v, want deck_export_review from deck output type", hints)
	}
	if !containsString(hints, "document_export_review") {
		t.Errorf("risk hints = %v, want document_export_review from pdf output type", hints)
	}
}

func TestListSkills_LabelKeyConvention(t *testing.T) {
	// G6 (2026-05-17). Pin two invariants on the i18n label
	// tokens shipped in the wire:
	//
	//  1. Every emitted item carries non-empty labelKey +
	//     descriptionKey. Empty keys would break the FE's
	//     `t(item.labelKey)` consumption pattern.
	//
	//  2. Today every key follows the convention
	//     `WorkAgent.modeSelector.modes.<agentMode>.{name,description}`.
	//     A future skill that opts out of the convention (e.g. a
	//     beta-namespaced label) would need to: (a) update its
	//     deriveLabelKey override path AND (b) carve out a per-skill
	//     case here. The convention check forces that conversation
	//     to surface in code review.
	//
	// Cross-references TestSkill_EveryUserFacingSkillHasFELocaleEntries
	// (G14) which separately verifies the keys named here actually
	// resolve in every locale file. Pair: this test pins what the
	// BE *emits*, that test pins what the FE *receives*.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/skills")
	var body struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, it := range body.Data.Items {
		if it.LabelKey == "" {
			t.Errorf("skill %q has empty labelKey — FE useTranslations would receive empty string", it.AgentMode)
		}
		if it.DescriptionKey == "" {
			t.Errorf("skill %q has empty descriptionKey — FE picker secondary line would be empty", it.AgentMode)
		}
		wantLabel := "WorkAgent.modeSelector.modes." + it.AgentMode + ".name"
		if it.LabelKey != wantLabel {
			t.Errorf("skill %q labelKey = %q, want %q (convention)", it.AgentMode, it.LabelKey, wantLabel)
		}
		wantDesc := "WorkAgent.modeSelector.modes." + it.AgentMode + ".description"
		if it.DescriptionKey != wantDesc {
			t.Errorf("skill %q descriptionKey = %q, want %q (convention)", it.AgentMode, it.DescriptionKey, wantDesc)
		}
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func hasInputKind(values []skills.InputRequirement, kind string) bool {
	for _, value := range values {
		if value.Kind == kind {
			return true
		}
	}
	return false
}

func findSkillCatalogTestItem(items []skillCatalogItem, agentMode string) *skillCatalogItem {
	for i := range items {
		if items[i].AgentMode == agentMode {
			return &items[i]
		}
	}
	return nil
}

func stringSliceEqual(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestListSkills_NoPostScriptsForLogo(t *testing.T) {
	// Negative case — logo today ships no post-generation script.
	// HasPostScripts should read false. If a future commit adds a
	// logo validator, update this test, not the source of truth.
	db := testutil.NewTestDB(t)
	installTestDB(t, db)
	engine := buildSkillCatalogEngine(t, 42)

	w := getRequest(engine, "/skills")
	var body struct {
		Data struct {
			Items []skillCatalogItem `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var logo *skillCatalogItem
	for i := range body.Data.Items {
		if body.Data.Items[i].AgentMode == "logo" {
			logo = &body.Data.Items[i]
			break
		}
	}
	if logo == nil {
		t.Fatalf("logo missing from catalog")
	}
	if logo.HasPostScripts {
		t.Error("logo should NOT carry HasPostScripts=true today (no scripts/*.py)")
	}
}
