// Canvas version service — parallels canvas_project_service.go and
// canvas_asset_service.go. Handlers under server/api/pro/tools stay
// thin HTTP adapters; every data-access rule, pagination default, and
// sentinel error for version rows lives here.
//
// Split:
//
//   Pure         NormalizeVersionSource, normalizeListVersionsInput —
//                unit-testable contracts the handlers used to inline.
//
//   DB-coupled   GetCanvasVersion / ListCanvasVersions / CreateCanvasVersion /
//                UpdateCanvasVersion — wrap MySQL under sentinel errors
//                (ErrCanvasVersionNotFound, ErrCanvasProjectNotFound,
//                ErrCanvasVersionReadOnly) so handlers can map 400/404
//                without scraping error strings.
//
// ErrCanvasProjectNotFound and ErrCanvasVersionReadOnly are reused from
// canvas_project_service.go — version and project services share the
// optimistic-locking invariant, so there is only one place to change
// the message or behaviour.

package canvas

import (
	"context"
	"errors"
	"strings"
	"time"

	"server/model"
	assetLedgerService "server/service/assetledger"
	projectService "server/service/project"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Version-specific sentinel. ErrCanvasProjectNotFound lives beside the
// project service; ErrCanvasVersionReadOnly lives there too because
// PatchElements already raises it during optimistic locking.
var ErrCanvasVersionNotFound = errors.New("canvas: version not found")
var ErrCanvasVersionConflict = errors.New("canvas: version conflict")

const (
	defaultListVersionsLimit = 20
	maxListVersionsLimit     = 100
	defaultVersionSource     = "manual"

	// optimisticLockTolerance widens the ExpectedProjectUpdatedAt gate
	// by 500ms to absorb MySQL DATETIME's second-precision rounding:
	// a client that observed updated_at=T can replay it on the next
	// patch without tripping a spurious conflict when the row's
	// truncated stored timestamp lands a fraction past T. Shared by
	// UpdateCanvasVersion (this file) and PatchElements
	// (canvas_project_service.go) — both encode the same invariant.
	optimisticLockTolerance = 500 * time.Millisecond
)

// CreateVersionInput mirrors the handler's createCanvasVersionRequest
// shape without JSON tags.
type CreateVersionInput struct {
	Document     model.JSONMap
	Message      string
	Source       string
	ThumbnailURL string
}

// ListVersionsInput is the pagination envelope. IncludeTotal defaults
// to true because the editor's version panel needs the count to render
// the "older" tail; hot-path polls opt out to skip the COUNT scan.
type ListVersionsInput struct {
	Page         int
	Limit        int
	IncludeTotal bool
}

// UpdateVersionInput is the patch envelope. Pointer fields are honoured
// only when non-nil so the caller can update `document` alone without
// overwriting message / source / thumbnail with zero values.
type UpdateVersionInput struct {
	Document                 model.JSONMap
	Message                  *string
	Source                   *string
	ThumbnailURL             *string
	ExpectedProjectUpdatedAt *time.Time
}

// NormalizeVersionSource strips whitespace and falls back to "manual"
// on empty — pins the default so the handler no longer inlines it.
func NormalizeVersionSource(raw string) string {
	source := strings.TrimSpace(raw)
	if source == "" {
		return defaultVersionSource
	}
	return source
}

// normalizeListVersionsInput clamps page/limit into the handler's old
// bounds (page≥1, 1≤limit≤100, default 20). Unexported because only
// ListCanvasVersions calls it.
func normalizeListVersionsInput(in ListVersionsInput) ListVersionsInput {
	out := in
	if out.Page < 1 {
		out.Page = 1
	}
	if out.Limit < 1 {
		out.Limit = defaultListVersionsLimit
	}
	if out.Limit > maxListVersionsLimit {
		out.Limit = maxListVersionsLimit
	}
	return out
}

// GetCanvasVersion loads one version by (projectID, version) scoped to
// uid. After M5-B-06 retire-T2 it reads from w_canvas_snapshot
// (historical metadata + frozen document) and overlays project.document
// for the latest version so autosave-fresh state surfaces. The
// w_canvas_version table is no longer touched by the read path.
//
// Semantics:
//   - N out of [1, project.latest_version] → ErrCanvasVersionNotFound.
//   - N == project.latest_version → metadata from snapshot row,
//     Document overridden with project.document (live mirror).
//   - N < project.latest_version → snapshot row returned verbatim
//     (frozen at "Save as new version" time).
func GetCanvasVersion(ctx context.Context, db *gorm.DB, uid int, projectID uint64, versionNum int) (model.CanvasVersion, error) {
	var project model.CanvasProject
	access, err := projectService.NewRepository(db.WithContext(ctx)).ResolveAccess(uint(projectID), uint(uid))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.CanvasVersion{}, ErrCanvasProjectNotFound
	}
	if err != nil {
		return model.CanvasVersion{}, err
	}
	if !access.CanView() {
		return model.CanvasVersion{}, ErrCanvasProjectNotFound
	}
	project = *access.Project
	if versionNum < 1 || versionNum > project.LatestVersion {
		return model.CanvasVersion{}, ErrCanvasVersionNotFound
	}

	var snap model.CanvasSnapshot
	if err := db.WithContext(ctx).
		Where("project_id = ? AND snapshot_no = ?", projectID, versionNum).
		First(&snap).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.CanvasVersion{}, ErrCanvasVersionNotFound
		}
		return model.CanvasVersion{}, err
	}

	v := model.CanvasVersion{
		ProjectID:    snap.ProjectID,
		Version:      snap.SnapshotNo,
		CreatedBy:    snap.CreatedBy,
		Source:       snap.Source,
		Message:      snap.Message,
		ElementCount: snap.ElementCount,
		ThumbnailURL: snap.ThumbnailURL,
		Document:     snap.Document,
	}
	v.Id = snap.Id
	v.CreatedAt = snap.CreatedAt
	// Snapshots are immutable, so UpdatedAt mirrors CreatedAt. Stage 5
	// may revisit if the API surface needs a distinct updated_at.
	v.UpdatedAt = snap.CreatedAt

	// For the latest version, override document/element_count with the
	// live state from the project row. project.document mirrors the
	// most recent autosave / patch / manual save, which is fresher than
	// the snapshot frozen at "Save as new version" time.
	if versionNum == project.LatestVersion {
		v.Document = project.Document
		v.ElementCount = project.ElementCount
	}
	return v, nil
}

// ListCanvasVersions paginates versions for a project scoped to uid.
// After M5-B-06 retire-T2 it reads from w_canvas_snapshot ordered by
// snapshot_no DESC. Documents are omitted from the row selection — the
// editor's version panel fetches each document on demand through
// GetCanvasVersion.
func ListCanvasVersions(ctx context.Context, db *gorm.DB, uid int, projectID uint64, in ListVersionsInput) (model.PaginatedResult[model.CanvasVersion], error) {
	in = normalizeListVersionsInput(in)

	exists, existsErr := canvasProjectExists(ctx, db, projectID, uid)
	if existsErr != nil {
		return model.PaginatedResult[model.CanvasVersion]{}, existsErr
	}
	if !exists {
		return model.PaginatedResult[model.CanvasVersion]{}, ErrCanvasProjectNotFound
	}

	query := db.WithContext(ctx).Model(&model.CanvasSnapshot{}).
		Where("project_id = ?", projectID)

	total := int64(0)
	if in.IncludeTotal {
		if err := query.Count(&total).Error; err != nil {
			return model.PaginatedResult[model.CanvasVersion]{}, err
		}
	}

	var snaps []model.CanvasSnapshot
	if err := query.
		Select("id, created_at, project_id, snapshot_no, created_by, source, message, element_count, thumbnail_url").
		Order("snapshot_no DESC").
		Offset((in.Page - 1) * in.Limit).
		Limit(in.Limit).
		Find(&snaps).Error; err != nil {
		return model.PaginatedResult[model.CanvasVersion]{}, err
	}

	items := make([]model.CanvasVersion, len(snaps))
	for i, s := range snaps {
		v := model.CanvasVersion{
			ProjectID:    s.ProjectID,
			Version:      s.SnapshotNo,
			CreatedBy:    s.CreatedBy,
			Source:       s.Source,
			Message:      s.Message,
			ElementCount: s.ElementCount,
			ThumbnailURL: s.ThumbnailURL,
		}
		v.Id = s.Id
		v.CreatedAt = s.CreatedAt
		v.UpdatedAt = s.CreatedAt
		items[i] = v
	}

	return model.PaginatedResult[model.CanvasVersion]{
		Items: items,
		Total: total,
		Page:  in.Page,
		Limit: in.Limit,
	}, nil
}

// writeLatestVersionDocument is the single dual-write seam for the
// "latest snapshot row + project row" document mirror. Every caller —
// CreateCanvasVersion (advances latest_version) and UpdateCanvasVersion
// (updates the existing latest) — must funnel through here.
//
// Why this exists: the project row's document is the editor's read path
// (live, autosave-fresh state); the snapshot row's document is the
// "frozen at save time" archival copy. The two MUST agree byte-for-byte
// at the moment of write, otherwise a later CreateCanvasVersion call
// would freeze a stale snapshot.document while the editor reads
// project.document. Centralising the write here means a future contributor
// can't split the pair and silently introduce split-brain.
//
// Both rows receive the same `doc` value computed from the caller's
// canonical input. No call site should ever construct two distinct
// JSONMap values for these two writes.
//
// snapshotUpdates lets the caller add metadata fields (message, source,
// thumbnail_url) on the snapshot row without losing the document/element
// count/schema_version invariant managed here.
//
// snapshotID and projectID are caller-supplied to keep this helper
// indifferent to whether the caller already loaded the rows for locking.
func writeLatestVersionDocument(
	tx *gorm.DB,
	projectID uint64,
	uid int,
	snapshotID uint,
	doc model.JSONMap,
	snapshotUpdates map[string]interface{},
	projectUpdatedAt time.Time,
) error {
	elementCount := ElementCountFromDocument(doc)
	schemaVersion := schemaVersionForStorage(doc)

	snapBase := map[string]interface{}{
		"document":       doc,
		"element_count":  elementCount,
		"schema_version": schemaVersion,
	}
	for k, v := range snapshotUpdates {
		// Refuse to let a caller smuggle a divergent document/count/schema
		// in via the metadata map — these three keys are owned by this
		// helper.
		if k == "document" || k == "element_count" || k == "schema_version" {
			continue
		}
		snapBase[k] = v
	}
	if err := tx.Model(&model.CanvasSnapshot{}).
		Where("id = ?", snapshotID).
		Updates(snapBase).Error; err != nil {
		return err
	}

	projectUpdates := map[string]interface{}{
		"updated_at":     projectUpdatedAt,
		"document":       doc,
		"schema_version": schemaVersion,
		"element_count":  elementCount,
	}
	return tx.Model(&model.CanvasProject{}).
		Where("id = ? AND deleted_at IS NULL", projectID).
		Updates(projectUpdates).Error
}

// MaxSnapshotsPerProject caps how many w_canvas_snapshot rows a single
// project can accumulate. Each row carries the full document blob, so
// without this gate a power-user that hits "save version" 10000 times
// turns one project into a multi-GB hotspot. The init snapshot
// (snapshot_no=1) is always preserved for provenance — only mid-lifetime
// snapshots are pruned. 200 chosen as the cap: covers anyone iterating
// daily for half a year without any UX-visible limit.
const MaxSnapshotsPerProject = 200

// pruneSnapshotsToCap hard-deletes the oldest non-init snapshots when a
// project's snapshot count exceeds MaxSnapshotsPerProject. Runs inside
// the caller's TX so the count check + delete are atomic against
// concurrent CreateCanvasVersion calls (the project row is FOR UPDATE-
// locked upstream).
//
// We deliberately do NOT cascade to w_user_asset_ledger thumbnail rows
// — those are referenced by version.Id and become harmless dead weight
// when the snapshot is gone. A future retention job can sweep them.
//
// P1-data #19: also preserve the snapshot_no currently referenced by
// w_canvas_share_snapshot.published_version. The share-snapshot
// row carries its own independent Document copy so the share-link
// read path keeps working even if this snapshot were pruned (see
// applyShareSnapshotForRead in canvas_project_service.go), but any
// audit JOIN from share_snapshot.published_version → canvas_snapshot.snapshot_no
// would dangle, and a future feature that wants to render "the live
// version vs the published version" diff would silently lose the
// published-side document if the share-snapshot row were ever lost.
// Cheap guard: NOT IN (published_version).
func pruneSnapshotsToCap(tx *gorm.DB, projectID uint64) error {
	var count int64
	if err := tx.Model(&model.CanvasSnapshot{}).
		Where("project_id = ?", projectID).
		Count(&count).Error; err != nil {
		return err
	}
	if count <= MaxSnapshotsPerProject {
		return nil
	}
	// Drop oldest-first, preserving snapshot_no=1 (the init row). Cap
	// the delete to (count - MaxSnapshotsPerProject) rows so we don't
	// over-prune if a future write somehow runs concurrently.
	toDrop := int(count - MaxSnapshotsPerProject)
	// Resolve the share-snapshot's published_version BEFORE the delete
	// query so the gorm sub-query stays a single SELECT (DB-side IN ()
	// with NULL behaves differently across MySQL/Postgres/SQLite;
	// resolving to Go-side avoids the foot-gun).
	var pinnedSnapshotNo int
	if err := tx.Model(&model.CanvasShareSnapshot{}).
		Where("project_id = ?", projectID).
		Select("published_version").
		Limit(1).
		Scan(&pinnedSnapshotNo).Error; err != nil {
		return err
	}
	q := tx.Model(&model.CanvasSnapshot{}).
		Select("snapshot_no").
		Where("project_id = ? AND snapshot_no > 1", projectID)
	if pinnedSnapshotNo > 1 {
		q = q.Where("snapshot_no <> ?", pinnedSnapshotNo)
	}
	return tx.Where(
		`project_id = ? AND snapshot_no > 1 AND snapshot_no IN (?)`,
		projectID,
		q.Order("snapshot_no ASC").Limit(toDrop),
	).Delete(&model.CanvasSnapshot{}).Error
}

// CreateCanvasVersion inserts a new version row and advances the
// parent project's LatestVersion atomically. Callers should pass a
// schema-migrated document (EnsureSchemaVersion) — the service does
// not re-run migration so validation errors surface at the handler
// where they can be mapped to PROMPT_BLOCKED-style codes.
//
// On a missing project the transaction returns ErrCanvasProjectNotFound
// so the handler can emit a 404 instead of a 500.
func CreateCanvasVersion(ctx context.Context, db *gorm.DB, uid int, projectID uint64, in CreateVersionInput) (model.CanvasVersion, error) {
	var created model.CanvasVersion
	doc, err := NormalizeCanvasDocumentForStorage(in.Document)
	if err != nil {
		return model.CanvasVersion{}, err
	}
	thumbnailURL, err := NormalizeCanvasReferenceURL(in.ThumbnailURL, false)
	if err != nil {
		return model.CanvasVersion{}, err
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireProjectEditAccess(ctx, tx, uid, uint(projectID)); err != nil {
			return err
		}
		var project model.CanvasProject
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", projectID).
			First(&project).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCanvasProjectNotFound
			}
			return err
		}

		// M5-B-06 retire-T3: the legacy version-row table (dropped by 20260534) is no longer written.
		// project.LatestVersion is the canonical "highest snapshot_no
		// allocated"; the project row is FOR UPDATE-locked above so
		// concurrent CreateCanvasVersion calls on the same project
		// serialise here and each see a consistent latest_version.
		nextSnapshotNo := project.LatestVersion + 1
		snapshot := model.CanvasSnapshot{
			ProjectID:     uint(projectID),
			SnapshotNo:    nextSnapshotNo,
			CreatedBy:     uid,
			Source:        NormalizeVersionSource(in.Source),
			Message:       in.Message,
			ElementCount:  ElementCountFromDocument(doc),
			ThumbnailURL:  thumbnailURL,
			Document:      doc,
			SchemaVersion: schemaVersionForStorage(doc),
		}
		if err := tx.Create(&snapshot).Error; err != nil {
			return err
		}

		if err := tx.Model(&model.CanvasProject{}).
			Where("id = ? AND deleted_at IS NULL", projectID).
			Updates(map[string]interface{}{
				"latest_version": nextSnapshotNo,
				"updated_at":     time.Now(),
				// Project row mirrors the new latest snapshot — read
				// path serves from here.
				"document":       doc,
				"schema_version": schemaVersionForStorage(doc),
				"element_count":  ElementCountFromDocument(doc),
			}).Error; err != nil {
			return err
		}
		// Compose return DTO from snapshot. Asset-ledger sync keys on
		// version.Id (= snap.Id post-retire) and version.Version (=
		// snapshot_no) so the ledger sees consistent ids.
		created.ProjectID = uint(projectID)
		created.Version = nextSnapshotNo
		created.CreatedBy = uid
		created.Source = snapshot.Source
		created.Message = snapshot.Message
		created.ElementCount = snapshot.ElementCount
		created.ThumbnailURL = snapshot.ThumbnailURL
		created.Document = doc
		created.Id = snapshot.Id
		created.CreatedAt = snapshot.CreatedAt
		created.UpdatedAt = snapshot.CreatedAt

		if err := assetLedgerService.New().SyncCanvasSnapshotThumbnailWithDB(tx, &created, &project); err != nil {
			return err
		}
		// Trim oldest non-init snapshots to the per-project cap. Runs
		// inside the same TX so the cap is enforced atomically with the
		// new insert; the project's FOR UPDATE lock above serialises
		// concurrent saves so the count check is consistent.
		if err := pruneSnapshotsToCap(tx, projectID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.CanvasVersion{}, err
	}
	return created, nil
}

// UpdateCanvasVersion patches a single version. Only the project's
// latest version is mutable — older rows raise ErrCanvasVersionReadOnly
// (the sentinel is shared with canvas_project_service.go because
// PatchElements enforces the same rule during optimistic locking).
//
// Pointer fields on UpdateVersionInput are honoured only when non-nil:
// Message is overwritten verbatim (trimmed); Source resets to "manual"
// only when explicitly cleared; ThumbnailURL is overwritten (trimmed).
// Document is always written because the route treats it as required.
//
// The project row's updated_at is bumped inside the same TX so the
// "recently edited" ordering on ListProjects stays correct.
func UpdateCanvasVersion(ctx context.Context, db *gorm.DB, uid int, projectID uint64, versionNum int, in UpdateVersionInput) (model.CanvasVersion, error) {
	var updated model.CanvasVersion
	doc, err := NormalizeCanvasDocumentForStorage(in.Document)
	if err != nil {
		return model.CanvasVersion{}, err
	}
	var thumbnailURL *string
	if in.ThumbnailURL != nil {
		normalized, err := NormalizeCanvasReferenceURL(*in.ThumbnailURL, false)
		if err != nil {
			return model.CanvasVersion{}, err
		}
		thumbnailURL = &normalized
	}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireProjectEditAccess(ctx, tx, uid, uint(projectID)); err != nil {
			return err
		}
		var project model.CanvasProject
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND deleted_at IS NULL", projectID).
			First(&project).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCanvasProjectNotFound
			}
			return err
		}

		// M5-B-06 retire-T3: validate version# against
		// project.LatestVersion directly. The legacy version-row table (dropped by 20260534) is
		// no longer read here; metadata (source / message / thumbnail)
		// lives on the snapshot row, live document lives on project.
		if versionNum < 1 || versionNum > project.LatestVersion {
			return ErrCanvasVersionNotFound
		}
		if versionNum != project.LatestVersion {
			return ErrCanvasVersionReadOnly
		}
		if in.ExpectedProjectUpdatedAt != nil && project.UpdatedAt.After(in.ExpectedProjectUpdatedAt.UTC().Add(optimisticLockTolerance)) {
			return ErrCanvasVersionConflict
		}

		// Snapshot row carries the metadata (source / message /
		// thumbnail). Update it alongside the project row's live
		// document so the "latest snapshot" stays in sync with the
		// autosave / manual-save state.
		var snap model.CanvasSnapshot
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND snapshot_no = ?", projectID, versionNum).
			First(&snap).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrCanvasVersionNotFound
			}
			return err
		}

		// Snapshot metadata that's NOT part of the document parity invariant
		// (message/source/thumbnail_url). The dual-write helper rejects any
		// document/element_count/schema_version smuggled in via this map.
		snapMetadata := map[string]interface{}{}
		if in.Message != nil {
			snapMetadata["message"] = strings.TrimSpace(*in.Message)
		}
		if in.Source != nil {
			source := strings.TrimSpace(*in.Source)
			if source != "" {
				snapMetadata["source"] = source
			}
		}
		if thumbnailURL != nil {
			snapMetadata["thumbnail_url"] = *thumbnailURL
		}

		now := time.Now()
		if err := writeLatestVersionDocument(
			tx, projectID, uid, snap.Id, doc, snapMetadata, now,
		); err != nil {
			return err
		}
		// Compose the return DTO from project + snapshot (post-update
		// state). The asset ledger keys on version.Id (= snap.Id
		// post-retire) and version.Version (= snapshot_no).
		updated.ProjectID = uint(projectID)
		updated.Version = versionNum
		updated.CreatedBy = snap.CreatedBy
		updated.Source = snap.Source
		updated.Message = snap.Message
		updated.ElementCount = ElementCountFromDocument(doc)
		updated.ThumbnailURL = snap.ThumbnailURL
		updated.Document = doc
		updated.Id = snap.Id
		updated.CreatedAt = snap.CreatedAt
		updated.UpdatedAt = now
		if in.Message != nil {
			updated.Message = strings.TrimSpace(*in.Message)
		}
		if in.Source != nil {
			source := strings.TrimSpace(*in.Source)
			if source != "" {
				updated.Source = source
			}
		}
		if thumbnailURL != nil {
			updated.ThumbnailURL = *thumbnailURL
		}
		if err := assetLedgerService.New().SyncCanvasSnapshotThumbnailWithDB(tx, &updated, &project); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.CanvasVersion{}, err
	}
	return updated, nil
}

// canvasProjectExists is the service-layer twin of the handler's
// canvasProjectExistsForUser helper. Duplicated (rather than imported)
// because the handler package imports this package, not the other way
// round; centralising would invert the dependency.
func canvasProjectExists(ctx context.Context, db *gorm.DB, projectID uint64, uid int) (bool, error) {
	ok, err := projectService.NewRepository(db.WithContext(ctx)).CanViewProject(uint(projectID), uint(uid))
	if err != nil {
		return false, err
	}
	return ok, nil
}
