// Canvas → w_canvas_shot sync (§13 M3-W7-02). The canvas document owns the
// user-facing truth about shots — order, timeline placement, links.
// w_canvas_shot gives each of those shots a stable DB id so downstream
// tooling (queue jobs, storyboard views, exports) can point at
// something smaller than the whole document.
//
// Split in two layers so tests don't need a live DB:
//
//   DiffShots(existing, incoming) → ShotSyncPlan
//       Pure. Produces the ordered set of INSERTs, UPDATEs, and
//       soft-DELETEs required to make `existing` match `incoming`.
//       Running the same plan twice is a no-op (idempotency) — that's
//       what the test suite pins down.
//
//   SyncShots(ctx, db, uid, projectID, incoming) → ShotSyncPlan, error
//       DB-backed wrapper. Loads existing rows, runs DiffShots, and
//       applies the plan inside a single transaction.

package canvas

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	"server/model"
	projectService "server/service/project"
)

// shotSyncColumns lists the database columns SyncShots is allowed to
// write on UPDATE. Kept in lock-step with shotFromLink (producer) and
// shotsEquivalent (equality check): whenever a field is added to the
// sync surface, add it to all three. Explicit column scoping prevents
// tx.Save from clobbering fields a future code path might write outside
// the canvas sync loop.
var shotSyncColumns = []string{
	"uid",
	"canvas_project_id",
	"local_card_id",
	"order_index",
	"title",
	"timeline_start_ms",
	"timeline_duration_ms",
	"status",
	"deleted_at",
}

// ShotSyncAction labels one entry in a sync plan. Using a string keeps
// logs and API responses readable when the plan is serialized.
type ShotSyncAction string

const (
	ShotSyncActionCreate    ShotSyncAction = "create"
	ShotSyncActionUpdate    ShotSyncAction = "update"
	ShotSyncActionSoftDel   ShotSyncAction = "soft-delete"
	ShotSyncActionUnchanged ShotSyncAction = "unchanged"
)

// ShotSyncPlan summarises what SyncShots will do (or has done) to
// reconcile canvas shots[] with w_canvas_shot rows. The embedded entries let
// callers inspect per-card decisions.
type ShotSyncPlan struct {
	Created   []model.Shot
	Updated   []model.Shot
	Deleted   []uint // IDs of soft-deleted rows
	Unchanged int

	// Entries captures the per-card decision in the order it was made.
	// Handy for API responses and for tests to assert on the plan.
	Entries []ShotSyncEntry
}

// ShotSyncEntry is one line in a ShotSyncPlan — the decision taken for
// a single ShotLink or existing row. The json tags pin the wire shape
// to the platform lowerCamelCase convention; SyncShots' HTTP handler
// returns these directly as `entries[]`, so any field rename here is
// a FE-visible wire-shape change. Marshal contract pinned by
// TestShotSyncEntryMarshalShape.
type ShotSyncEntry struct {
	LocalCardID string         `json:"localCardId"`
	Action      ShotSyncAction `json:"action"`
	ShotID      uint           `json:"shotId"` // 0 for plans that haven't executed yet
}

// DiffShots compares `incoming` canvas ShotLinks against the rows
// currently in the database for a project (`existing`) and returns a
// plan that would bring the DB in line with the canvas document.
//
// Idempotency: calling DiffShots twice with the same inputs (and
// applying the plan in between) yields an empty plan the second time.
func DiffShots(
	uid int,
	canvasProjectID uint,
	existing []model.Shot,
	incoming []ShotLink,
) ShotSyncPlan {
	plan := ShotSyncPlan{}

	// Index existing rows by LocalCardID so we can intersect in one
	// pass. Skip rows with an empty key — they shouldn't exist in a
	// healthy DB but we don't want to accidentally re-use them.
	existingByKey := make(map[string]model.Shot, len(existing))
	for _, row := range existing {
		if strings.TrimSpace(row.LocalCardID) == "" {
			continue
		}
		existingByKey[row.LocalCardID] = row
	}

	seenKeys := make(map[string]struct{}, len(incoming))
	for i, link := range incoming {
		key := strings.TrimSpace(link.LocalCardID)
		if key == "" {
			// Drop unkeyed shots rather than create anonymous rows.
			continue
		}
		if _, dup := seenKeys[key]; dup {
			// Canvas should never emit the same LocalCardID twice;
			// treat the second occurrence as a no-op so we never
			// generate conflicting INSERTs.
			continue
		}
		seenKeys[key] = struct{}{}

		desired := shotFromLink(uid, canvasProjectID, link, i)

		row, ok := existingByKey[key]
		if !ok {
			plan.Created = append(plan.Created, desired)
			plan.Entries = append(plan.Entries, ShotSyncEntry{
				LocalCardID: key,
				Action:      ShotSyncActionCreate,
			})
			continue
		}
		if row.DeletedAt != nil {
			// Previously soft-deleted — treat as recreate. Preserve the
			// existing row ID so callers can clear deleted_at in place.
			desired.GraMODEL = row.GraMODEL
			desired.DeletedAt = nil
			plan.Updated = append(plan.Updated, desired)
			plan.Entries = append(plan.Entries, ShotSyncEntry{
				LocalCardID: key,
				Action:      ShotSyncActionUpdate,
				ShotID:      row.Id,
			})
			continue
		}
		if shotsEquivalent(row, desired) {
			plan.Unchanged++
			plan.Entries = append(plan.Entries, ShotSyncEntry{
				LocalCardID: key,
				Action:      ShotSyncActionUnchanged,
				ShotID:      row.Id,
			})
			continue
		}
		desired.GraMODEL = row.GraMODEL
		desired.DeletedAt = row.DeletedAt
		plan.Updated = append(plan.Updated, desired)
		plan.Entries = append(plan.Entries, ShotSyncEntry{
			LocalCardID: key,
			Action:      ShotSyncActionUpdate,
			ShotID:      row.Id,
		})
	}

	// Anything in existing that the canvas no longer mentions gets
	// soft-deleted. Already-deleted rows stay put.
	for key, row := range existingByKey {
		if _, kept := seenKeys[key]; kept {
			continue
		}
		if row.DeletedAt != nil {
			continue
		}
		plan.Deleted = append(plan.Deleted, row.Id)
		plan.Entries = append(plan.Entries, ShotSyncEntry{
			LocalCardID: key,
			Action:      ShotSyncActionSoftDel,
			ShotID:      row.Id,
		})
	}

	return plan
}

// SyncShots loads existing rows, applies DiffShots, and commits the
// plan in one transaction. Returns the realised plan (with DB IDs
// filled in for creates) so the caller can return or log it.
func SyncShots(
	ctx context.Context,
	db *gorm.DB,
	uid int,
	canvasProjectID uint,
	incoming []ShotLink,
) (ShotSyncPlan, error) {
	if db == nil {
		return ShotSyncPlan{}, errors.New("canvas: SyncShots requires a non-nil db")
	}
	if uid <= 0 || canvasProjectID == 0 {
		return ShotSyncPlan{}, errors.New("canvas: SyncShots requires uid and canvasProjectID")
	}
	if err := ValidateShotLinksForSync(incoming); err != nil {
		return ShotSyncPlan{}, err
	}
	access, err := projectService.NewRepository(db.WithContext(ctx)).ResolveAccess(canvasProjectID, uint(uid))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ShotSyncPlan{}, ErrCanvasProjectNotFound
	}
	if err != nil {
		return ShotSyncPlan{}, err
	}
	if !access.CanEdit() {
		return ShotSyncPlan{}, ErrCanvasProjectNotFound
	}
	projectOwnerUID := access.Project.UID

	var existing []model.Shot
	if err := db.WithContext(ctx).
		Unscoped(). // include soft-deleted so we can revive by LocalCardID
		Where("canvas_project_id = ?", canvasProjectID).
		Find(&existing).Error; err != nil {
		return ShotSyncPlan{}, err
	}

	plan := DiffShots(projectOwnerUID, canvasProjectID, existing, incoming)

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range plan.Created {
			if err := tx.Create(&plan.Created[i]).Error; err != nil {
				return err
			}
			// Backfill the entry's ShotID so API responses
			// can point at the freshly inserted row.
			key := plan.Created[i].LocalCardID
			for j := range plan.Entries {
				if plan.Entries[j].LocalCardID == key &&
					plan.Entries[j].Action == ShotSyncActionCreate {
					plan.Entries[j].ShotID = plan.Created[i].Id
					break
				}
			}
		}
		for i := range plan.Updated {
			// Select scopes the UPDATE to columns the sync owns; without
			// it Save would write every column on model.Shot and clobber
			// any field (e.g. Title) a future code path might populate
			// outside this loop.
			if err := tx.Select(shotSyncColumns).Save(&plan.Updated[i]).Error; err != nil {
				return err
			}
		}
		if len(plan.Deleted) > 0 {
			if err := tx.Model(&model.Shot{}).
				Where("id IN ?", plan.Deleted).
				Update("deleted_at", gorm.Expr("CURRENT_TIMESTAMP")).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ShotSyncPlan{}, err
	}
	return plan, nil
}

// shotFromLink translates a ShotLink + its position in the incoming
// slice into the persisted shape. OrderIndex on the link wins over the
// slice position when set (non-zero) so the canvas caller can express
// explicit ordering independent of array order; otherwise we fall back
// to the slice index so the first pass still writes a deterministic
// order.
func shotFromLink(uid int, projectID uint, link ShotLink, position int) model.Shot {
	order := link.OrderIndex
	if order == 0 {
		order = position
	}
	return model.Shot{
		UID:                uid,
		CanvasProjectID:    projectID,
		LocalCardID:        strings.TrimSpace(link.LocalCardID),
		OrderIndex:         order,
		TimelineStartMs:    link.TimelineStart,
		TimelineDurationMs: link.TimelineDuration,
		Status:             model.ShotStatusDraft,
	}
}

// shotsEquivalent compares only the fields the sync controls — so
// DB-managed fields like CreatedAt / UpdatedAt don't cause spurious
// UPDATEs. Keep this list in lock-step with shotFromLink.
func shotsEquivalent(a, b model.Shot) bool {
	if a.UID != b.UID ||
		a.CanvasProjectID != b.CanvasProjectID ||
		a.LocalCardID != b.LocalCardID ||
		a.OrderIndex != b.OrderIndex ||
		a.Title != b.Title ||
		a.Status != b.Status {
		return false
	}
	return int64PtrEqual(a.TimelineStartMs, b.TimelineStartMs) &&
		int64PtrEqual(a.TimelineDurationMs, b.TimelineDurationMs)
}

func int64PtrEqual(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
