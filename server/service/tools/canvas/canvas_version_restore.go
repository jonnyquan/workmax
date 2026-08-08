// Canvas version restore — C-09 (closed 2026-05-15 by owner). Restores
// a historical w_canvas_snapshot row as the project's new live
// document, while preserving the source snapshot untouched and
// writing an explicit audit-trail snapshot (source="restored",
// message="Restored from vN") so the version history records the
// intent.
//
// Why this exists as a first-class service surface:
//
//   Without it, clients have to fake restore with
//   GetCanvasVersion(N) → UpdateCanvasVersion(latest, {document: that}).
//   That path silently rewrites the latest snapshot row with the old
//   document, destroying its own history; loses the audit trail
//   ("this was a restore, not an autosave"); and races the
//   ExpectedProjectUpdatedAt gate because the client must round-trip
//   to compute the right timestamp.
//
//   RestoreCanvasSnapshot does the three writes atomically inside one
//   transaction: source snapshot stays intact, new audit snapshot is
//   allocated at LatestVersion+1, project row is repointed.
//
// Asset reference validation reuses ExtractAssetReferences from
// element_orphan_scanner.go (the C-06 Stage 1 walker) — same 3 channels
// (metadata.globalAssetId / SessionAttempt.AssetID+ResultAssetID /
// V1Fields URL refs). DanglingAssetsPolicy controls whether dangling
// refs abort the restore (default — safer; caller sees the list and
// can decide) or pass through with a warning payload.

package canvas

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"server/model"
	assetLedgerService "server/service/assetledger"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DanglingAssetsPolicy controls how RestoreCanvasSnapshot reacts when
// the source snapshot references w_global_asset rows that have been
// deleted / tombstoned since the snapshot was taken.
type DanglingAssetsPolicy string

const (
	// DanglingAssetsAbort is the default: the restore aborts and the
	// caller receives ErrCanvasRestoreDanglingAssets + the dangling
	// reference list. Safer — prevents a click-through restore from
	// silently shipping a project full of broken images.
	DanglingAssetsAbort DanglingAssetsPolicy = "abort"

	// DanglingAssetsAllow proceeds with the restore even when refs
	// don't resolve. The dangling list is returned in the result so
	// the caller can warn the user. Use after an explicit
	// confirmation step.
	DanglingAssetsAllow DanglingAssetsPolicy = "allow"

	// versionSourceRestored is the snapshot.source tag for the audit
	// row this operation creates. Kept here (vs NormalizeVersionSource
	// in canvas_version_service.go) because it's the only call site
	// that sets it.
	versionSourceRestored = "restored"
)

// RestoreSnapshotInput is the patch envelope. ExpectedProjectUpdatedAt
// mirrors UpdateCanvasVersion's optimistic-lock gate so a client that
// just observed updated_at can pass it through.
type RestoreSnapshotInput struct {
	ExpectedProjectUpdatedAt *time.Time
	DanglingAssets           DanglingAssetsPolicy
}

// DanglingAssetRef names one unresolved asset reference. Exactly one
// of GlobalAssetID / URL is set — the channel the walker picked it up
// from. JSON tags match the wire shape the handler emits.
type DanglingAssetRef struct {
	GlobalAssetID uint   `json:"globalAssetId,omitempty"`
	URL           string `json:"url,omitempty"`
}

// RestoreSnapshotResult is what RestoreCanvasSnapshot returns on
// success. On an ErrCanvasRestoreDanglingAssets failure the same
// struct is populated up to the abort point — Project + NewSnapshot
// are zero-valued, but RestoredFrom + DanglingAssets carry the
// diagnosis.
type RestoreSnapshotResult struct {
	Project        model.CanvasProject  `json:"project"`
	NewSnapshot    model.CanvasVersion  `json:"newSnapshot"`
	RestoredFrom   int                  `json:"restoredFrom"`
	DanglingAssets []DanglingAssetRef   `json:"danglingAssets,omitempty"`
}

// ErrCanvasRestoreDanglingAssets is returned when the source snapshot
// references assets that no longer resolve and the caller's
// DanglingAssetsPolicy is "abort" (the default). The accompanying
// RestoreSnapshotResult carries the list so the caller can surface a
// confirmation flow.
var ErrCanvasRestoreDanglingAssets = errors.New("canvas: restore source references dangling assets")

// diffAssetReferences subtracts the live asset set from the
// reference set extracted from a snapshot document. Pure function so
// the matching logic is unit-testable without DB.
//
// Returns dangling refs sorted (numeric ids first, ascending, then
// URLs lexicographically) so call results are deterministic.
func diffAssetReferences(refs *AssetReferenceSet, active []model.GlobalAsset) []DanglingAssetRef {
	if refs == nil {
		return nil
	}
	liveIDs := make(map[uint]struct{}, len(active))
	liveURLs := make(map[string]struct{}, len(active)*2)
	for i := range active {
		a := &active[i]
		liveIDs[a.Id] = struct{}{}
		if a.URL != "" {
			liveURLs[a.URL] = struct{}{}
		}
		if a.ThumbURL != "" {
			liveURLs[a.ThumbURL] = struct{}{}
		}
	}
	var dangling []DanglingAssetRef
	for id := range refs.GlobalAssetIDs {
		if _, ok := liveIDs[id]; !ok {
			dangling = append(dangling, DanglingAssetRef{GlobalAssetID: id})
		}
	}
	for url := range refs.URLs {
		if _, ok := liveURLs[url]; !ok {
			dangling = append(dangling, DanglingAssetRef{URL: url})
		}
	}
	sort.Slice(dangling, func(i, j int) bool {
		a, b := dangling[i], dangling[j]
		// ID-typed entries before URL-only entries — gives a stable
		// "missing-by-id first, then missing-by-url" reading order.
		// URL-only entries have GlobalAssetID == 0 (default zero).
		aHasID := a.GlobalAssetID > 0
		bHasID := b.GlobalAssetID > 0
		if aHasID != bHasID {
			return aHasID
		}
		if aHasID {
			return a.GlobalAssetID < b.GlobalAssetID
		}
		return a.URL < b.URL
	})
	return dangling
}

// RestoreCanvasSnapshot rewinds projectID to the document content of
// snapshot sourceVersionNum. Atomic — inside one transaction, with
// FOR UPDATE lock on the project row, so concurrent edits cannot
// interleave.
//
// Behaviour:
//
//   - Validates sourceVersionNum ∈ [1, project.LatestVersion]; older
//     snapshots that were pruned by pruneSnapshotsToCap return
//     ErrCanvasVersionNotFound.
//   - Honours ExpectedProjectUpdatedAt with the shared
//     optimisticLockTolerance window.
//   - Validates asset references via ExtractAssetReferences; aborts
//     by default if any are dangling.
//   - Allocates a NEW snapshot at LatestVersion+1 with the source's
//     document, source="restored", and message="Restored from vN" so
//     ListCanvasVersions surfaces the operation as an explicit row.
//     The source snapshot is never mutated.
//   - Bumps project.LatestVersion and re-syncs the asset ledger
//     thumbnail.
//   - Runs pruneSnapshotsToCap inside the same TX; the new audit
//     snapshot is the newest row, so it survives any pruning trigger.
//     A source snapshot that pushes the project over the cap can
//     itself be pruned post-restore, but its content has already been
//     projected into the new audit row — no data loss.
func RestoreCanvasSnapshot(
	ctx context.Context,
	db *gorm.DB,
	uid int,
	projectID uint64,
	sourceVersionNum int,
	in RestoreSnapshotInput,
) (RestoreSnapshotResult, error) {
	var result RestoreSnapshotResult
	if in.DanglingAssets == "" {
		in.DanglingAssets = DanglingAssetsAbort
	}

	txErr := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

		if sourceVersionNum < 1 || sourceVersionNum > project.LatestVersion {
			return ErrCanvasVersionNotFound
		}
		if in.ExpectedProjectUpdatedAt != nil &&
			project.UpdatedAt.After(in.ExpectedProjectUpdatedAt.UTC().Add(optimisticLockTolerance)) {
			return ErrCanvasVersionConflict
		}

		var source model.CanvasSnapshot
		if err := tx.Where("project_id = ? AND snapshot_no = ?", projectID, sourceVersionNum).
			First(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// Source pruned even though it was within [1, latest].
				// Possible if the cap shrunk between earlier reads and
				// now — treat as "version not found" so the client
				// retries / picks a different anchor.
				return ErrCanvasVersionNotFound
			}
			return err
		}

		// Defensive re-normalization: source was normalized at store
		// time, but a schema-contract change between then and now would
		// surface here rather than silently flowing into the new
		// snapshot. Idempotent for already-v2 docs.
		sourceDoc, err := NormalizeCanvasDocumentForStorage(source.Document)
		if err != nil {
			return err
		}

		// Asset-reference validation (reuses C-06 walker).
		refs := extractAssetReferences(sourceDoc)
		var liveAssets []model.GlobalAsset
		if err := tx.Model(&model.GlobalAsset{}).
			Select("id, url, thumb_url").
			Where("source_table = ? AND project_id = ?", "canvas_project_file", projectID).
			Where("deleted_at IS NULL AND status <> ?", model.GlobalAssetStatusDeleted).
			Find(&liveAssets).Error; err != nil {
			return err
		}
		dangling := diffAssetReferences(refs, liveAssets)
		if len(dangling) > 0 && in.DanglingAssets == DanglingAssetsAbort {
			// Populate the result before returning the sentinel so the
			// caller can surface the dangling list to the user.
			result.RestoredFrom = sourceVersionNum
			result.DanglingAssets = dangling
			return ErrCanvasRestoreDanglingAssets
		}

		// Allocate the audit snapshot at LatestVersion+1.
		newSnapshotNo := project.LatestVersion + 1
		elementCount := ElementCountFromDocument(sourceDoc)
		schemaVersion := schemaVersionForStorage(sourceDoc)
		now := time.Now()
		auditSnap := model.CanvasSnapshot{
			ProjectID:     uint(projectID),
			SnapshotNo:    newSnapshotNo,
			CreatedBy:     uid,
			Source:        versionSourceRestored,
			Message:       fmt.Sprintf("Restored from v%d", sourceVersionNum),
			ElementCount:  elementCount,
			ThumbnailURL:  source.ThumbnailURL,
			Document:      sourceDoc,
			SchemaVersion: schemaVersion,
		}
		if err := tx.Create(&auditSnap).Error; err != nil {
			return err
		}

		if err := tx.Model(&model.CanvasProject{}).
			Where("id = ? AND deleted_at IS NULL", projectID).
			Updates(map[string]interface{}{
				"latest_version": newSnapshotNo,
				"document":       sourceDoc,
				"schema_version": schemaVersion,
				"element_count":  elementCount,
				"thumbnail_url":  source.ThumbnailURL,
				"updated_at":     now,
			}).Error; err != nil {
			return err
		}

		// Compose the success result. project mirrors post-update state
		// so callers don't have to re-fetch.
		project.LatestVersion = newSnapshotNo
		project.Document = sourceDoc
		project.SchemaVersion = schemaVersion
		project.ElementCount = elementCount
		project.ThumbnailURL = source.ThumbnailURL
		project.UpdatedAt = now

		newVersion := model.CanvasVersion{
			ProjectID:    uint(projectID),
			Version:      newSnapshotNo,
			CreatedBy:    uid,
			Source:       versionSourceRestored,
			Message:      auditSnap.Message,
			ElementCount: elementCount,
			ThumbnailURL: source.ThumbnailURL,
			Document:     sourceDoc,
		}
		newVersion.Id = auditSnap.Id
		newVersion.CreatedAt = auditSnap.CreatedAt
		newVersion.UpdatedAt = auditSnap.CreatedAt

		if err := assetLedgerService.New().SyncCanvasSnapshotThumbnailWithDB(tx, &newVersion, &project); err != nil {
			return err
		}
		if err := pruneSnapshotsToCap(tx, projectID); err != nil {
			return err
		}

		result.Project = project
		result.NewSnapshot = newVersion
		result.RestoredFrom = sourceVersionNum
		result.DanglingAssets = dangling // empty on the happy path
		return nil
	})

	if txErr != nil {
		if errors.Is(txErr, ErrCanvasRestoreDanglingAssets) {
			// Carry the dangling list out — caller distinguishes via
			// errors.Is and reads result.DanglingAssets.
			return result, txErr
		}
		return RestoreSnapshotResult{}, txErr
	}
	return result, nil
}
