//go:build desktop

package desktop

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

const pinTestThreadUUID = "de305d54-75b4-431b-adb2-eb6b9e546401"

func TestPinThreadLiftsItToTheTop(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	// Two threads; the OLDER one gets pinned and must lead the listing.
	oldUUID := pinTestThreadUUID
	newUUID := "de305d54-75b4-431b-adb2-eb6b9e546402"
	seedLocalThreadForDelete(t, db, localSingleUserUID, oldUUID)
	seedLocalThreadForDelete(t, db, localSingleUserUID, newUUID)
	if err := db.Exec(
		`UPDATE w_workagent_thread SET updated_at = ? WHERE uuid = ?`,
		time.Now().UTC().Add(-48*time.Hour).Format(time.RFC3339Nano), oldUUID,
	).Error; err != nil {
		t.Fatal(err)
	}

	resp, body := localAccountsRequest(
		t, http.MethodPost, base+"/agent/threads/"+oldUUID+"/pin", "tok", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"pinned":true`) {
		t.Fatalf("pin: %d %s", resp.StatusCode, body)
	}
	// Idempotent: pinning twice is pinned, not an error.
	if resp, _ := localAccountsRequest(
		t, http.MethodPost, base+"/agent/threads/"+oldUUID+"/pin", "tok", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("re-pin: %d", resp.StatusCode)
	}

	rows, err := ListLocalThreads(db, localSingleUserUID, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].UUID != oldUUID || !rows[0].Pinned {
		t.Fatalf("pinned thread must lead the listing, got %+v", rows)
	}
	if rows[1].Pinned {
		t.Fatalf("unpinned thread reported pinned: %+v", rows[1])
	}

	// Unpin: recency order returns.
	resp, body = localAccountsRequest(
		t, http.MethodDelete, base+"/agent/threads/"+oldUUID+"/pin", "tok", "")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"pinned":false`) {
		t.Fatalf("unpin: %d %s", resp.StatusCode, body)
	}
	rows, err = ListLocalThreads(db, localSingleUserUID, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].UUID != newUUID || rows[0].Pinned || rows[1].Pinned {
		t.Fatalf("after unpin the listing must be recency-ordered, got %+v", rows)
	}
}

func TestPinThreadOwnershipAndScope(t *testing.T) {
	base, db, _, _ := newDeleteFixture(t)
	// A foreign identity's thread is not found, not forbidden.
	if err := db.Exec(
		`INSERT INTO w_workagent_thread (uid, uuid, name, cloud_sync_state) VALUES (?, ?, 'Not yours', 'local')`,
		localSingleUserUID+7, pinTestThreadUUID,
	).Error; err != nil {
		t.Fatal(err)
	}
	resp, _ := localAccountsRequest(
		t, http.MethodPost, base+"/agent/threads/"+pinTestThreadUUID+"/pin", "tok", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign pin: %d", resp.StatusCode)
	}
	if resp, _ := localAccountsRequest(
		t, http.MethodPost, base+"/agent/threads/not-a-uuid/pin", "tok", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad uuid: %d", resp.StatusCode)
	}

	// Pins are per-identity: the same thread uuid pinned by uid A must not
	// surface as pinned in uid B's listing.
	mine := "de305d54-75b4-431b-adb2-eb6b9e546403"
	seedLocalThreadForDelete(t, db, localSingleUserUID, mine)
	if resp, _ := localAccountsRequest(
		t, http.MethodPost, base+"/agent/threads/"+mine+"/pin", "tok", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("pin mine: %d", resp.StatusCode)
	}
	rows, err := ListLocalThreads(db, localSingleUserUID+7, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Pinned {
			t.Fatalf("another identity sees a pin it never set: %+v", row)
		}
	}
}
