//go:build desktop

package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	migrationsdesktop "server/desktop/migrations_desktop"
)

// openMindsTestDB runs the real migrations: the store and the DDL must agree,
// and the only way to keep that honest is to make the test read the same SQL
// the boot path applies.
func openMindsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "minds.db")), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := migrationsdesktop.Apply(db); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return db
}

// fakeMindKnowledge implements KnowledgeIndex + MindKnowledge in memory:
// feeds accumulate per (mind, title), and MindSources reports them back the
// way the store would.
type fakeMindKnowledge struct {
	mu         sync.Mutex
	feeds      []fakeMindFeed
	failFeed   error
	failForget error
	forgotten  []string
}

// ForgetMind erases a mind's memory. The fake records the call as well as
// applying it: the ORDER of erase-then-delete is the property the handler has
// to get right, and a fake that only applied the effect could not show it.
func (f *fakeMindKnowledge) ForgetMind(_ context.Context, mindID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failForget != nil {
		return 0, f.failForget
	}
	f.forgotten = append(f.forgotten, mindID)
	kept := f.feeds[:0]
	removed := 0
	for _, feed := range f.feeds {
		if feed.mindID == mindID {
			removed += feed.chunks
			continue
		}
		kept = append(kept, feed)
	}
	f.feeds = kept
	return removed, nil
}

func (f *fakeMindKnowledge) IndexFile(_ context.Context, _ uint64, _ int64) error { return nil }
func (f *fakeMindKnowledge) RemoveFile(_ context.Context, _ int64) (int, error)    { return 0, nil }
func (f *fakeMindKnowledge) RemoveTurn(_ context.Context, _ string) (int, error)   { return 0, nil }

type fakeMindFeed struct {
	uid    uint64
	mindID string
	title  string
	text   string
	chunks int
}

func (f *fakeMindKnowledge) IndexMindMaterial(_ context.Context, uid uint64, mindID, title, text string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFeed != nil {
		return 0, f.failFeed
	}
	chunks := 1 + len(text)/500
	f.feeds = append(f.feeds, fakeMindFeed{uid: uid, mindID: mindID, title: title, text: text, chunks: chunks})
	return chunks, nil
}

func (f *fakeMindKnowledge) MindSources(_ context.Context, uid uint64, mindID string) ([]MindSourceStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	byTitle := map[string]*MindSourceStat{}
	var order []string
	for _, feed := range f.feeds {
		if feed.uid != uid || feed.mindID != mindID {
			continue
		}
		stat, ok := byTitle[feed.title]
		if !ok {
			stat = &MindSourceStat{Title: feed.title, IndexedAt: time.Now().Unix()}
			byTitle[feed.title] = stat
			order = append(order, feed.title)
		}
		stat.Chunks += feed.chunks
	}
	out := make([]MindSourceStat, 0, len(order))
	for _, title := range order {
		out = append(out, *byTitle[title])
	}
	return out, nil
}

// bootMindsFixture boots the sidecar with a migrated DB, the mind store, and
// no TokenStore — the unscoped identity (uid 0), which is a resolved identity
// for requestOwner and keeps the tests independent of keychain stubbing.
func bootMindsFixture(t *testing.T, knowledge KnowledgeIndex) (baseURL, token string, db *gorm.DB) {
	t.Helper()
	db = openMindsTestDB(t)
	gateway, err := NewModelGateway()
	if err != nil {
		t.Fatalf("NewModelGateway: %v", err)
	}
	srv, err := NewServer(ServerConfig{
		SidecarVersion: "minds-test",
		LocalToken:     "minds-tok",
		DB:             db,
		ModelGateway:   gateway,
		Minds:          NewMindStore(db),
		KnowledgeIndex: knowledge,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return "http://" + srv.listener.Addr().String(), "minds-tok", db
}

func mindsRequest(t *testing.T, method, url, tok, body string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Local-Token", tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(resp.Body)
	return resp, payload
}

func TestMindsListSeedsActiveDefault(t *testing.T) {
	base, tok, _ := bootMindsFixture(t, nil)
	resp, body := mindsRequest(t, http.MethodGet, base+"/minds", tok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var parsed MindListDTO
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Count != 1 || len(parsed.Items) != 1 {
		t.Fatalf("expected the seeded default mind, got %s", body)
	}
	mind := parsed.Items[0]
	if mind.Name != defaultMindName || !mind.Active {
		t.Fatalf("default mind wrong: %s", body)
	}
	if !mindIDShape.MatchString(mind.ID) {
		t.Fatalf("mind id %q does not match the marking convention's shape", mind.ID)
	}
}

func TestMindsCreateSelectFlow(t *testing.T) {
	base, tok, _ := bootMindsFixture(t, nil)

	resp, body := mindsRequest(t, http.MethodPost, base+"/minds", tok,
		`{"name":"Payroll mind","role_hint":"Owns compensation questions","model_override":"claude-opus-4.1"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d: %s", resp.StatusCode, body)
	}
	var created Mind
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if created.Active {
		t.Fatal("a created mind must not take over on its own")
	}
	if created.ModelOverride != "claude-opus-4.1" || created.RoleHint == "" {
		t.Fatalf("created mind lost its intent: %s", body)
	}

	resp, body = mindsRequest(t, http.MethodPost, base+"/minds/"+created.ID+"/select", tok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("select status %d: %s", resp.StatusCode, body)
	}

	resp, body = mindsRequest(t, http.MethodGet, base+"/minds", tok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status %d: %s", resp.StatusCode, body)
	}
	var parsed MindListDTO
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Count != 2 || !parsed.Items[0].Active || parsed.Items[0].ID != created.ID {
		t.Fatalf("the selected mind must lead the list as active: %s", body)
	}

	// Selecting a mind that does not exist is a 404, not a switch.
	resp, _ = mindsRequest(t, http.MethodPost,
		base+"/minds/mind-de305d54-75b4-431b-adb2-eb6b9e546014/select", tok, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("select unknown: status %d, want 404", resp.StatusCode)
	}
	// And a malformed id never reaches the store.
	resp, _ = mindsRequest(t, http.MethodPost, base+"/minds/nope/select", tok, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("select malformed id: status %d, want 400", resp.StatusCode)
	}
}

func TestMindsCreateRejections(t *testing.T) {
	base, tok, _ := bootMindsFixture(t, nil)
	cases := []struct {
		name   string
		body   string
		status int
	}{
		{"empty name", `{"name":"  "}`, http.StatusBadRequest},
		{"unknown field", `{"name":"M","skills":["x"]}`, http.StatusBadRequest},
		{"control character", "{\"name\":\"a\\tb\"}", http.StatusBadRequest},
		{"model with a space", `{"name":"M","model_override":"two words"}`, http.StatusBadRequest},
		{"trailing json", `{"name":"M"} {}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := mindsRequest(t, http.MethodPost, base+"/minds", tok, tc.body)
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, tc.status, body)
			}
		})
	}
}

func TestMindStatusWithoutKnowledge(t *testing.T) {
	base, tok, _ := bootMindsFixture(t, nil)
	resp, body := mindsRequest(t, http.MethodGet, base+"/minds", tok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status %d: %s", resp.StatusCode, body)
	}
	var list MindListDTO
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("parse: %v", err)
	}
	mind := list.Items[0]

	resp, body = mindsRequest(t, http.MethodGet, base+"/minds/"+mind.ID+"/status", tok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var status MindStatusDTO
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if status.Mind.ID != mind.ID {
		t.Fatalf("status is for %q, want %q", status.Mind.ID, mind.ID)
	}
	// No cgo knowledge wiring in this fixture: retrieval says so, and the
	// memory is an empty list rather than an error.
	if status.Retrieval != "unavailable" {
		t.Fatalf("retrieval = %q, want unavailable", status.Retrieval)
	}
	if status.Memory.Chunks != 0 || len(status.Memory.Sources) != 0 {
		t.Fatalf("memory must be empty without a knowledge store: %s", body)
	}
	if status.Model.Source != "identity" || status.Model.Label == "" {
		t.Fatalf("model must fall back to the identity: %s", body)
	}
}

func TestMindFeedAndStatusWithKnowledge(t *testing.T) {
	knowledge := &fakeMindKnowledge{}
	base, tok, _ := bootMindsFixture(t, knowledge)
	resp, body := mindsRequest(t, http.MethodGet, base+"/minds", tok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status %d: %s", resp.StatusCode, body)
	}
	var list MindListDTO
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("parse: %v", err)
	}
	mind := list.Items[0]

	resp, body = mindsRequest(t, http.MethodPost, base+"/minds/"+mind.ID+"/feed", tok,
		`{"title":"Compensation bands","text":"The 2026 bands: L4 starts at 180k base."}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("feed status %d: %s", resp.StatusCode, body)
	}
	var fed MindFeedResult
	if err := json.Unmarshal(body, &fed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !fed.Fed || fed.Title != "Compensation bands" || fed.Chunks < 1 {
		t.Fatalf("feed result wrong: %s", body)
	}
	if len(knowledge.feeds) != 1 || knowledge.feeds[0].mindID != mind.ID {
		t.Fatalf("feed reached the knowledge store under the wrong mind: %+v", knowledge.feeds)
	}

	resp, body = mindsRequest(t, http.MethodGet, base+"/minds/"+mind.ID+"/status", tok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var status MindStatusDTO
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if status.Retrieval != "local" {
		t.Fatalf("retrieval = %q, want local", status.Retrieval)
	}
	if status.Memory.Chunks != fed.Chunks || len(status.Memory.Sources) != 1 ||
		status.Memory.Sources[0].Title != "Compensation bands" {
		t.Fatalf("the fed material must show up in the mind's memory: %s", body)
	}

	// Feed validation: neither an untitled nor an empty material may be indexed.
	for _, bad := range []string{
		`{"title":"","text":"x"}`,
		`{"title":"t","text":"  "}`,
		`{"title":"t","text":"x","extra":1}`,
	} {
		resp, _ = mindsRequest(t, http.MethodPost, base+"/minds/"+mind.ID+"/feed", tok, bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("feed %s: status %d, want 400", bad, resp.StatusCode)
		}
	}
	if len(knowledge.feeds) != 1 {
		t.Fatalf("invalid feeds must not reach the store: %+v", knowledge.feeds)
	}
}

func TestMindFeedRequiresKnowledge(t *testing.T) {
	base, tok, _ := bootMindsFixture(t, nil)
	resp, body := mindsRequest(t, http.MethodGet, base+"/minds", tok, "")
	var list MindListDTO
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("parse: %v", err)
	}
	resp, body = mindsRequest(t, http.MethodPost, base+"/minds/"+list.Items[0].ID+"/feed", tok,
		`{"title":"t","text":"x"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("feed without knowledge: status %d, want 503: %s", resp.StatusCode, body)
	}
}

// "This machine cannot embed anything" and "this material is bad" are
// different answers and the user acts on them differently. A build with no
// embedding model must not tell someone their document was rejected.
func TestMindFeedSeparatesAMissingModelFromABadMaterial(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		says   string
	}{
		{"no assets on this platform", errKnowledgeAssetsUnavailable, http.StatusServiceUnavailable, "no embedding model"},
		{"assets still downloading", errKnowledgeAssetsFetching, http.StatusServiceUnavailable, "still downloading"},
		{"the indexer really failed", errors.New("vec0 write failed"), http.StatusBadGateway, "could not be indexed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			knowledge := &fakeMindKnowledge{failFeed: tc.err}
			base, tok, _ := bootMindsFixture(t, knowledge)
			_, body := mindsRequest(t, http.MethodGet, base+"/minds", tok, "")
			var list MindListDTO
			if err := json.Unmarshal(body, &list); err != nil {
				t.Fatalf("parse: %v", err)
			}
			resp, body := mindsRequest(t, http.MethodPost, base+"/minds/"+list.Items[0].ID+"/feed", tok,
				`{"title":"t","text":"some material"}`)
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, tc.status, body)
			}
			if !strings.Contains(string(body), tc.says) {
				t.Fatalf("error must say %q, got %s", tc.says, body)
			}
		})
	}
}

func TestMindStatusRejectsUnknownMind(t *testing.T) {
	base, tok, _ := bootMindsFixture(t, nil)
	resp, _ := mindsRequest(t, http.MethodGet,
		base+"/minds/mind-de305d54-75b4-431b-adb2-eb6b9e546014/status", tok, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status for unknown mind: %d, want 404", resp.StatusCode)
	}
	resp, _ = mindsRequest(t, http.MethodGet, base+"/minds/nope/status", tok, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status for malformed id: %d, want 400", resp.StatusCode)
	}
}

// The marking convention the whole feature rests on: a mind's memory is the
// knowledge whose source_id carries its id. Pinned here so a change to either
// side of the contract fails in this package, not in a user's index.
func TestMindSourceIDConvention(t *testing.T) {
	id := "mind-de305d54-75b4-431b-adb2-eb6b9e546014"
	if !mindIDShape.MatchString(id) {
		t.Fatal("the canonical mind id shape drifted")
	}
	if strings.ContainsAny(id, ":%_") {
		t.Fatal("a mind id must be safe inside a source_id prefix")
	}
}

// Active is the accessor a turn depends on, so its failure DIRECTION is the
// property worth pinning: no mind chosen and no store at all must both answer
// "no opinion" rather than an error, because a turn that could not read a
// preference should still answer the question it was asked.
func TestMindStoreActiveFailsTowardsNoOpinion(t *testing.T) {
	db := openMindsTestDB(t)
	store := NewMindStore(db)

	// The seeded default is the active one, and it is what a turn sees.
	active, ok, err := store.Active(0)
	if err != nil || !ok {
		t.Fatalf("Active on a fresh identity = %+v %v %v, want the seeded default", active, ok, err)
	}
	if active.Name != defaultMindName || !active.Active {
		t.Fatalf("active mind = %+v, want the seeded default", active)
	}

	// Selecting moves it, so the turn follows the choice rather than the
	// creation order.
	created, err := store.Create(0, MindPut{Name: "Compensation", ModelOverride: "specialist-model"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Select(0, created.ID); err != nil {
		t.Fatalf("Select: %v", err)
	}
	active, ok, err = store.Active(0)
	if err != nil || !ok || active.ID != created.ID {
		t.Fatalf("Active after Select = %+v %v %v, want the selected mind", active, ok, err)
	}
	if active.ModelOverride != "specialist-model" {
		t.Fatalf("the model a turn would use = %q", active.ModelOverride)
	}

	// A store that was never wired answers "no opinion" without touching a
	// database it does not have.
	if _, ok, err := (*MindStore)(nil).Active(0); ok || err != nil {
		t.Fatalf("nil store = %v %v, want no opinion and no error", ok, err)
	}
	if _, ok, err := NewMindStore(nil).Active(0); ok || err != nil {
		t.Fatalf("store without a db = %v %v, want no opinion and no error", ok, err)
	}
}

// activeMindPolicy is the bridge between the mind table and the tool-loop
// engines, which cannot import this package. It is small, and it is the only
// place that decides WHICH of a mind's fields govern a turn — so an omission
// here is a mind that exists, is selected, and changes nothing.
func TestActiveMindPolicyCarriesBothHalves(t *testing.T) {
	db := openMindsTestDB(t)
	store := NewMindStore(db)
	created, err := store.Create(0, MindPut{
		Name:          "Compensation",
		RoleHint:      "Answer only in bullet points.",
		ModelOverride: "specialist-model",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Select(0, created.ID); err != nil {
		t.Fatalf("Select: %v", err)
	}

	policy := activeMindPolicy(db)(0)
	if policy.Name != "Compensation" {
		t.Errorf("name = %q; without it the transcript cannot tell two minds apart", policy.Name)
	}
	if policy.Model != "specialist-model" {
		t.Errorf("model = %q, want the mind's", policy.Model)
	}
	// The half that changes how the model WORKS, as opposed to what it knows.
	// Dropping it leaves a mind that can be created, selected and taught, and
	// that never once behaves differently.
	if policy.Persona != "Answer only in bullet points." {
		t.Errorf("persona = %q, want the mind's role hint", policy.Persona)
	}

	// The seeded default mind asks for nothing, so the identity's own
	// configuration governs — which is what "a mind is optional" has to mean.
	if err := store.Select(0, defaultMindOf(t, store).ID); err != nil {
		t.Fatalf("Select default: %v", err)
	}
	if policy := activeMindPolicy(db)(0); policy.Model != "" {
		t.Errorf("the default mind must not choose a model: %+v", policy)
	}

	// No database is not an error path a turn should notice.
	if activeMindPolicy(nil) != nil {
		t.Error("a nil database must produce no resolver at all")
	}
}

func defaultMindOf(t *testing.T, store *MindStore) Mind {
	t.Helper()
	all, err := store.List(0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, m := range all {
		if m.Name == defaultMindName {
			return m
		}
	}
	t.Fatal("the seeded default mind is missing")
	return Mind{}
}

// Editing exists so a role hint is not written once and forever: correcting
// one by recreating the mind would cost everything it was taught.
func TestMindUpdateReplacesTheDescribablePartsOnly(t *testing.T) {
	base, tok, _ := bootMindsFixture(t, nil)
	_, body := mindsRequest(t, http.MethodPost, base+"/minds", tok,
		`{"name":"Compensation","role_hint":"Be terse.","model_override":"m1"}`)
	var created Mind
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("parse create: %v", err)
	}
	if _, b := mindsRequest(t, http.MethodPost, base+"/minds/"+created.ID+"/select", tok, ""); b == nil {
		t.Fatal("select failed")
	}

	resp, body := mindsRequest(t, http.MethodPut, base+"/minds/"+created.ID, tok,
		`{"name":"Comp","role_hint":"Answer in bullet points.","model_override":""}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status %d: %s", resp.StatusCode, body)
	}
	var updated Mind
	if err := json.Unmarshal(body, &updated); err != nil {
		t.Fatalf("parse update: %v", err)
	}
	if updated.Name != "Comp" || updated.RoleHint != "Answer in bullet points." {
		t.Fatalf("update did not take: %+v", updated)
	}
	if updated.ModelOverride != "" {
		t.Fatalf("a form that sends an empty model means the identity's: %+v", updated)
	}
	// Being active is its own act with its own endpoint. Folding it in here
	// would let a rename quietly take over the workspace.
	if !updated.Active {
		t.Fatalf("an edit must not change which mind is active: %+v", updated)
	}

	if resp, _ := mindsRequest(t, http.MethodPut, base+"/minds/"+created.ID, tok, `{"name":""}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a nameless mind = %d, want 400", resp.StatusCode)
	}
	if resp, _ := mindsRequest(t, http.MethodPut, base+"/minds/mind-00000000-0000-4000-8000-000000000000", tok,
		`{"name":"Ghost"}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("editing a mind that does not exist = %d, want 404", resp.StatusCode)
	}
}

// Deleting a mind deletes everything it was taught, and the ORDER is the
// property: memory first, row only if that succeeded.
//
// Retrieval keeps the active mind's material and drops every other mind's, so
// a chunk whose mind no longer exists can never be reached by anything again.
// Leaving it would not be preserving knowledge; it would be keeping
// unreachable rows on the user's disk forever.
func TestMindDeleteTakesItsMemoryWithIt(t *testing.T) {
	fake := &fakeMindKnowledge{}
	base, tok, _ := bootMindsFixture(t, fake)

	_, body := mindsRequest(t, http.MethodPost, base+"/minds", tok, `{"name":"Compensation"}`)
	var created Mind
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("parse create: %v", err)
	}
	if resp, b := mindsRequest(t, http.MethodPost, base+"/minds/"+created.ID+"/feed", tok,
		`{"title":"Bands","text":"L4 is one hundred eighty thousand"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("feed status %d: %s", resp.StatusCode, b)
	}

	resp, body := mindsRequest(t, http.MethodDelete, base+"/minds/"+created.ID, tok, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status %d: %s", resp.StatusCode, body)
	}
	if len(fake.forgotten) != 1 || fake.forgotten[0] != created.ID {
		t.Fatalf("the mind's memory must be erased with it: %v", fake.forgotten)
	}
	if len(fake.feeds) != 0 {
		t.Fatalf("material survived the delete: %+v", fake.feeds)
	}
	if _, listBody := mindsRequest(t, http.MethodGet, base+"/minds", tok, ""); strings.Contains(string(listBody), created.ID) {
		t.Fatalf("the deleted mind is still listed: %s", listBody)
	}

	// The last mind cannot go: something has to be active, and reseeding a
	// default the user just deleted would be stranger than refusing.
	var list MindListDTO
	_, listBody := mindsRequest(t, http.MethodGet, base+"/minds", tok, "")
	if err := json.Unmarshal(listBody, &list); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if list.Count != 1 {
		t.Fatalf("expected only the default to remain: %s", listBody)
	}
	before := len(fake.forgotten)
	if resp, _ := mindsRequest(t, http.MethodDelete, base+"/minds/"+list.Items[0].ID, tok, ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("deleting the last mind = %d, want 409", resp.StatusCode)
	}
	// And a refused delete must not have erased anything on the way to being
	// refused — the check runs before the memory does.
	if len(fake.forgotten) != before {
		t.Fatalf("a refused delete erased memory anyway: %v", fake.forgotten)
	}
}

// Deleting the ACTIVE mind hands the flag on. An identity with no active mind
// falls back to unscoped retrieval, which is a behaviour change nobody asked
// for arriving silently.
func TestMindDeleteMovesTheActiveFlag(t *testing.T) {
	fake := &fakeMindKnowledge{}
	base, tok, db := bootMindsFixture(t, fake)
	_, body := mindsRequest(t, http.MethodPost, base+"/minds", tok, `{"name":"Compensation"}`)
	var created Mind
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("parse create: %v", err)
	}
	if resp, _ := mindsRequest(t, http.MethodPost, base+"/minds/"+created.ID+"/select", tok, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("select failed")
	}
	if resp, b := mindsRequest(t, http.MethodDelete, base+"/minds/"+created.ID, tok, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status %d: %s", resp.StatusCode, b)
	}
	active, ok, err := NewMindStore(db).Active(0)
	if err != nil || !ok {
		t.Fatalf("after deleting the active mind there must still be one: %v %v", ok, err)
	}
	if active.Name != defaultMindName {
		t.Fatalf("the flag should land on the oldest survivor, got %+v", active)
	}
}

// The store's own guards, not the handler's. Delete is a public method and the
// handler is not its only conceivable caller, so its rules are asserted where
// they live rather than only through the route that happens to check first.
func TestMindStoreDeleteGuardsItself(t *testing.T) {
	db := openMindsTestDB(t)
	store := NewMindStore(db)

	// The seeded default is the only mind, and it may not go.
	only := defaultMindOf(t, store)
	if err := store.Delete(0, only.ID); !errors.Is(err, errMindLastOne) {
		t.Fatalf("deleting the only mind = %v, want errMindLastOne", err)
	}
	if err := store.CanDelete(0, only.ID); !errors.Is(err, errMindLastOne) {
		t.Fatalf("CanDelete on the only mind = %v, want errMindLastOne", err)
	}
	if _, err := store.Get(0, only.ID); err != nil {
		t.Fatalf("a refused delete must leave the mind: %v", err)
	}

	// With a second one, both succeed — and CanDelete stays a question, not an
	// action: asking must not delete.
	second, err := store.Create(0, MindPut{Name: "Second"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.CanDelete(0, second.ID); err != nil {
		t.Fatalf("CanDelete = %v, want nil", err)
	}
	if _, err := store.Get(0, second.ID); err != nil {
		t.Fatalf("CanDelete must not delete anything: %v", err)
	}
	if err := store.Delete(0, second.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(0, second.ID); !errors.Is(err, errMindNotFound) {
		t.Fatalf("after Delete, Get = %v, want not found", err)
	}

	// An id that is not a mind id never reaches a query.
	if err := store.Delete(0, "../../etc/passwd"); !errors.Is(err, errMindID) {
		t.Fatalf("Delete with a malformed id = %v, want errMindID", err)
	}
}
