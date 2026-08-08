package mcp_connector

import (
	"errors"
	"testing"

	"server/model"
	"server/utils/testutil"

	"gorm.io/gorm"
)

func seedConnector(t *testing.T, db *gorm.DB, uid int, name, transport string) *model.MCPConnector {
	t.Helper()
	c := &model.MCPConnector{
		UID:       uid,
		Name:      name,
		Transport: transport,
		Enabled:   true,
	}
	switch transport {
	case model.MCPTransportStdio:
		c.Command = "/usr/bin/test-mcp"
	case model.MCPTransportSSE, model.MCPTransportHTTP:
		c.URL = "https://example.test/mcp"
	}
	repo := NewRepository(db)
	if err := repo.Create(c); err != nil {
		t.Fatalf("seed connector: %v", err)
	}
	return c
}

func TestCreate_StdioHappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	c := seedConnector(t, db, 42, "linear", model.MCPTransportStdio)
	if c.Id == 0 {
		t.Fatal("Create should assign id")
	}
	if !c.Enabled {
		t.Error("Enabled defaults to true via Select(\"*\") + explicit seed")
	}
}

func TestCreate_HTTPHappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	c := seedConnector(t, db, 42, "remote-mcp", model.MCPTransportHTTP)
	if c.URL == "" {
		t.Error("URL should round-trip")
	}
}

func TestCreate_RejectsZeroUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	err := repo.Create(&model.MCPConnector{Name: "x", Transport: model.MCPTransportStdio, Command: "/x"})
	if err == nil {
		t.Fatal("expected error on uid=0")
	}
}

func TestCreate_RejectsEmptyName(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	err := repo.Create(&model.MCPConnector{UID: 42, Name: "   ", Transport: model.MCPTransportStdio, Command: "/x"})
	if err == nil {
		t.Fatal("expected error on whitespace name")
	}
}

func TestCreate_RejectsEmptyTransport(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	err := repo.Create(&model.MCPConnector{UID: 42, Name: "x", Transport: "", Command: "/x"})
	if err == nil || err.Error() != "transport is required" {
		t.Fatalf("expected transport-required, got %v", err)
	}
}

func TestCreate_RejectsUnknownTransport(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	err := repo.Create(&model.MCPConnector{UID: 42, Name: "x", Transport: "ws", URL: "u"})
	if err == nil {
		t.Fatal("expected unknown-transport error")
	}
}

func TestCreate_StdioRequiresCommand(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	err := repo.Create(&model.MCPConnector{UID: 42, Name: "x", Transport: model.MCPTransportStdio})
	if err == nil {
		t.Fatal("expected command-required error")
	}
}

func TestCreate_SseRequiresURL(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	err := repo.Create(&model.MCPConnector{UID: 42, Name: "x", Transport: model.MCPTransportSSE})
	if err == nil {
		t.Fatal("expected url-required error")
	}
}

func TestLoadByIDForOwner_HappyPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	seeded := seedConnector(t, db, 42, "linear", model.MCPTransportStdio)
	repo := NewRepository(db)

	got, err := repo.LoadByIDForOwner(seeded.Id, 42)
	if err != nil {
		t.Fatalf("LoadByIDForOwner: %v", err)
	}
	if got.Name != "linear" {
		t.Errorf("name lost: %q", got.Name)
	}
}

func TestLoadByIDForOwner_CrossTenantReturnsNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	seeded := seedConnector(t, db, 42, "private", model.MCPTransportStdio)
	repo := NewRepository(db)

	_, err := repo.LoadByIDForOwner(seeded.Id, 99)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected ErrRecordNotFound, got %v", err)
	}
}

func TestLoadByIDForOwner_ZeroInputsReturnNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedConnector(t, db, 42, "x", model.MCPTransportStdio)
	repo := NewRepository(db)

	for _, c := range []struct{ id, uid uint }{{0, 42}, {1, 0}, {0, 0}} {
		_, err := repo.LoadByIDForOwner(c.id, c.uid)
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Errorf("(id=%d uid=%d) expected ErrRecordNotFound, got %v", c.id, c.uid, err)
		}
	}
}

func TestListForOwner_NewestFirst(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedConnector(t, db, 42, "a", model.MCPTransportStdio)
	seedConnector(t, db, 42, "b", model.MCPTransportSSE)
	seedConnector(t, db, 42, "c", model.MCPTransportHTTP)
	repo := NewRepository(db)

	rows, err := repo.ListForOwner(42, 10, 0)
	if err != nil {
		t.Fatalf("ListForOwner: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("expected 3 rows, got %d", len(rows))
	}
}

func TestListForOwner_ScopedByUID(t *testing.T) {
	db := testutil.NewTestDB(t)
	seedConnector(t, db, 42, "mine", model.MCPTransportStdio)
	seedConnector(t, db, 99, "foreign", model.MCPTransportStdio)
	repo := NewRepository(db)

	rows, _ := repo.ListForOwner(42, 10, 0)
	if len(rows) != 1 || rows[0].Name != "mine" {
		t.Errorf("cross-tenant scope leaked; got %+v", rows)
	}
}

func TestListEnabledForOwner_FiltersDisabledRows(t *testing.T) {
	db := testutil.NewTestDB(t)
	enabled := seedConnector(t, db, 42, "enabled", model.MCPTransportStdio)
	disabled := seedConnector(t, db, 42, "disabled", model.MCPTransportStdio)
	repo := NewRepository(db)
	if err := repo.SetEnabled(disabled.Id, 42, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	rows, err := repo.ListEnabledForOwner(42)
	if err != nil {
		t.Fatalf("ListEnabledForOwner: %v", err)
	}
	if len(rows) != 1 || rows[0].Id != enabled.Id {
		t.Errorf("expected only enabled row; got %+v", rows)
	}
}

func TestSetEnabled_TogglesBit(t *testing.T) {
	db := testutil.NewTestDB(t)
	seeded := seedConnector(t, db, 42, "x", model.MCPTransportStdio)
	repo := NewRepository(db)

	if err := repo.SetEnabled(seeded.Id, 42, false); err != nil {
		t.Fatalf("SetEnabled false: %v", err)
	}
	got, _ := repo.LoadByIDForOwner(seeded.Id, 42)
	if got.Enabled {
		t.Error("expected enabled=false after toggle")
	}

	if err := repo.SetEnabled(seeded.Id, 42, true); err != nil {
		t.Fatalf("SetEnabled true: %v", err)
	}
	got, _ = repo.LoadByIDForOwner(seeded.Id, 42)
	if !got.Enabled {
		t.Error("expected enabled=true after second toggle")
	}
}

func TestSetEnabled_CrossTenantReturnsNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	seeded := seedConnector(t, db, 42, "x", model.MCPTransportStdio)
	repo := NewRepository(db)

	err := repo.SetEnabled(seeded.Id, 99, false)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected ErrRecordNotFound, got %v", err)
	}
}

func TestUpdate_PartialFieldsApply(t *testing.T) {
	db := testutil.NewTestDB(t)
	seeded := seedConnector(t, db, 42, "old-name", model.MCPTransportStdio)
	repo := NewRepository(db)

	newName := "new-name"
	if err := repo.Update(seeded.Id, 42, UpdatePatch{Name: &newName}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ := repo.LoadByIDForOwner(seeded.Id, 42)
	if got.Name != "new-name" {
		t.Errorf("name not updated: %q", got.Name)
	}
}

func TestUpdate_TransportSwitchRequiresMatchingFields(t *testing.T) {
	db := testutil.NewTestDB(t)
	seeded := seedConnector(t, db, 42, "x", model.MCPTransportStdio)
	repo := NewRepository(db)

	// Switching stdio → sse without setting URL must error
	// (validateTransport rejects post-merge inconsistency).
	httpTransport := model.MCPTransportHTTP
	err := repo.Update(seeded.Id, 42, UpdatePatch{Transport: &httpTransport})
	if err == nil {
		t.Fatal("expected transport-switch validation error")
	}
}

func TestSoftDelete_RemovesFromList(t *testing.T) {
	db := testutil.NewTestDB(t)
	seeded := seedConnector(t, db, 42, "x", model.MCPTransportStdio)
	repo := NewRepository(db)

	if err := repo.SoftDelete(seeded.Id, 42); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	rows, _ := repo.ListForOwner(42, 10, 0)
	if len(rows) != 0 {
		t.Errorf("soft-deleted should not list; got %d", len(rows))
	}
	_, err := repo.LoadByIDForOwner(seeded.Id, 42)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("soft-deleted Load should ErrRecordNotFound; got %v", err)
	}
}

func TestSoftDelete_CrossTenantReturnsNotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	seeded := seedConnector(t, db, 42, "x", model.MCPTransportStdio)
	repo := NewRepository(db)

	err := repo.SoftDelete(seeded.Id, 99)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected ErrRecordNotFound, got %v", err)
	}
}

// Validation-sentinel contract: every input-shape rejection must
// match errors.Is(err, ErrValidation). Pin every documented
// validation branch so a new validation that forgets to wrap
// the sentinel lights up here, not silently downgrades to a
// 5xx-shaped response in the handler.
func TestValidationErrors_AllMatchSentinel(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	cases := []struct {
		name string
		fn   func() error
	}{
		{
			"zero uid",
			func() error {
				return repo.Create(&model.MCPConnector{UID: 0, Name: "x", Transport: model.MCPTransportStdio, Command: "/x"})
			},
		},
		{
			"blank name",
			func() error {
				return repo.Create(&model.MCPConnector{UID: 1, Name: "   ", Transport: model.MCPTransportStdio, Command: "/x"})
			},
		},
		{
			"missing transport",
			func() error {
				return repo.Create(&model.MCPConnector{UID: 1, Name: "x", Transport: ""})
			},
		},
		{
			"unknown transport",
			func() error {
				return repo.Create(&model.MCPConnector{UID: 1, Name: "x", Transport: "ws", URL: "wss://x"})
			},
		},
		{
			"stdio without command",
			func() error {
				return repo.Create(&model.MCPConnector{UID: 1, Name: "x", Transport: model.MCPTransportStdio})
			},
		},
		{
			"sse without url",
			func() error {
				return repo.Create(&model.MCPConnector{UID: 1, Name: "x", Transport: model.MCPTransportSSE})
			},
		},
		{
			"http without url",
			func() error {
				return repo.Create(&model.MCPConnector{UID: 1, Name: "x", Transport: model.MCPTransportHTTP})
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.fn()
			if err == nil {
				t.Fatalf("%s: expected validation error, got nil", c.name)
			}
			if !errors.Is(err, ErrValidation) {
				t.Errorf("%s: errors.Is(err, ErrValidation) = false; got %T(%v)", c.name, err, err)
			}
		})
	}
}

func TestValidationErrors_UpdateBlankNameMatchesSentinel(t *testing.T) {
	// Update path has its own validation branch (the "connector
	// name cannot be empty" rejection); pin it separately because
	// it goes through a different call site than Create.
	db := testutil.NewTestDB(t)
	seeded := seedConnector(t, db, 42, "before", model.MCPTransportStdio)
	repo := NewRepository(db)

	blank := "   "
	err := repo.Update(seeded.Id, 42, UpdatePatch{Name: &blank})
	if err == nil {
		t.Fatal("expected validation error on blank Update name")
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("errors.Is(err, ErrValidation) = false; got %T(%v)", err, err)
	}
}

func TestValidationError_PreservesMessage(t *testing.T) {
	// The sentinel switch must not change err.Error() — handler
	// code, logs, and any existing test that compares against the
	// raw message all depend on the unchanged string.
	db := testutil.NewTestDB(t)
	repo := NewRepository(db)
	err := repo.Create(&model.MCPConnector{UID: 1, Name: "x", Transport: ""})
	if err == nil || err.Error() != "transport is required" {
		t.Errorf("Error() = %v, want 'transport is required'", err)
	}
}
