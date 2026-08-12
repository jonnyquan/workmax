//go:build desktop

package desktop

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"gorm.io/gorm"

	"server/desktop/knowledge/assets"
	localinference "server/desktop/local_inference"
	localrender "server/desktop/local_render"
)

// The L3c knowledge package is cgo-only (it embeds ONNX Runtime), and this
// package is deliberately not. Both consumers of RAG are already structural
// interfaces — KnowledgeIndex here, localinference.KnowledgeHooks there — so the
// only thing missing was a construction seam that itself carries no cgo.
//
// That seam is knowledgeProvider: bootstrap_cgo.go installs it from an init
// when the binary is built with cgo, and leaves it nil otherwise. A non-cgo
// build therefore still compiles this whole package and boots with RAG off,
// which is what keeps `go test ./desktop/...` runnable without a C toolchain
// and lets a shell binary opt out of the native resource dependency entirely.

// KnowledgeDeps is what the RAG stack needs from the rest of the boot.
type KnowledgeDeps struct {
	DB    *gorm.DB
	Files *localrender.Store

	// ResourcesDir is the packaged native asset root — inside the app bundle
	// on a signed build, which is why nothing is ever written there. Empty →
	// the knowledge package falls back to WORKMAX_RESOURCES_DIR, then a
	// "resources" directory beside the working directory.
	ResourcesDir string

	// DataDir is the per-user data root. Downloaded assets land under it,
	// because it is the one location that is both writable and per-user.
	DataDir string
}

// KnowledgeWiring is the RAG surface the rest of the boot consumes. A zero
// value is the RAG-off configuration and is valid everywhere: Index nil
// means uploads are stored but not indexed, Hooks nil means turns are neither
// indexed nor retrieved against.
type KnowledgeWiring struct {
	Index KnowledgeIndex
	Hooks localinference.KnowledgeHooks

	// Close releases the ONNX Runtime environment. Nil when RAG is off.
	// Boot.Shutdown calls it before the process exits — see the ordering
	// note there for why skipping it aborts the process.
	Close func() error

	// Dim is the embedding dimensionality, logged at boot so a support
	// transcript records which model actually loaded.
	Dim int
}

// knowledgeProvider is installed by bootstrap_cgo.go on cgo builds.
var knowledgeProvider func(KnowledgeDeps) (KnowledgeWiring, error)

// resolveKnowledge builds the RAG stack when it is both compiled in and
// resolvable, and otherwise returns the RAG-off zero value. It never fails
// the boot: a desktop that cannot embed is a desktop without retrieval, not a
// desktop that refuses to start.
func resolveKnowledge(deps KnowledgeDeps) KnowledgeWiring {
	if knowledgeProvider == nil {
		log.Printf("knowledge: RAG disabled (built without cgo)")
		return KnowledgeWiring{}
	}
	wiring, err := knowledgeProvider(deps)
	if err != nil {
		log.Printf("knowledge: RAG disabled (%v)", err)
		return KnowledgeWiring{}
	}
	// "available", not "loaded": the model is read on first use, so saying it
	// is enabled here would misreport both the memory in use and when the
	// startup cost is actually paid.
	log.Printf("knowledge: local RAG available (%d-dim embeddings, loaded on first use)", wiring.Dim)
	return wiring
}

// knowledgeDownloadDirName is where downloaded native assets live, under the
// data dir. Separate from the packaged resources directory on purpose: that
// one is inside a code-signed app bundle and read-only.
const knowledgeDownloadDirName = "knowledge_resources"

// knowledgeAssetRetry is how long a failed acquisition is remembered before
// another call is allowed to try again. Long enough that a machine with no
// hosted assets does not re-read a manifest on every keystroke; short enough
// that plugging in a network cable fixes it within one coffee.
const knowledgeAssetRetry = 10 * time.Minute

// errKnowledgeAssetsFetching is returned while a background download runs. It
// is a "not yet", not a failure: the turn that triggered it proceeds without
// retrieval and a later one will have it.
var errKnowledgeAssetsFetching = errors.New("knowledge: embedding assets are downloading in the background")

// errKnowledgeAssetsUnavailable marks every OTHER way the native embedding
// assets are not usable: nothing pinned for this platform, a manifest that
// will not resolve, a download that failed.
//
// It exists so a caller can tell "this material is bad" from "this machine
// cannot embed anything right now" — a distinction the user feels directly.
// Feeding a mind on a build with no assets used to answer 502, which reads as
// "your document was rejected"; the truth is that nothing could have been
// indexed, and that is a 503 with a reason.
var errKnowledgeAssetsUnavailable = errors.New("knowledge: embedding assets are unavailable")

// knowledgeAssets acquires the native embedding assets without ever blocking a
// boot or a turn on the network.
//
// The order matters and encodes the whole delivery policy:
//
//  1. Already on disk (packaged with the app, downloaded earlier, or put there
//     by the user)? Use them. This is the only path an offline machine takes,
//     and it costs three stat calls.
//  2. Not on disk, but a manifest pins them for this platform? Start ONE
//     background download, bounded by a timeout and a size cap, and tell the
//     caller to come back later.
//  3. Not on disk and nothing pinned? Say so, once, with both fixes named.
//     This is the state the open-source tree ships in, and making it explicit
//     is the point — it used to be a silent ErrUnsupportedPlatform that read
//     as "RAG is broken" to everyone who hit it.
//
// The three collaborators are injected so this policy is testable — including
// on a CGO_ENABLED=0 build, where the real resolver does not exist.
type knowledgeAssets struct {
	// dir is where downloads land and are looked for.
	dir string

	// present reports nil when the assets are usable right now.
	present func() error

	// plan resolves the manifest for dir.
	plan func(dir string) (assets.Plan, error)

	// fetch downloads a plan's missing assets into dir.
	fetch func(ctx context.Context, dir string, p assets.Plan) error

	now func() time.Time

	mu       sync.Mutex
	fetching bool
	lastErr  error
	retryAt  time.Time
	logged   map[string]bool
}

func newKnowledgeAssets(dir string, present func() error) *knowledgeAssets {
	return &knowledgeAssets{
		dir:     dir,
		present: present,
		plan:    assets.PlanFor,
		fetch: func(ctx context.Context, dir string, p assets.Plan) error {
			return assets.EnsurePlan(ctx, dir, p, nil)
		},
		now: time.Now,
	}
}

// ensure reports nil when the assets are ready to load, and otherwise an error
// describing what is being done about it.
func (k *knowledgeAssets) ensure() error {
	if err := k.present(); err == nil {
		return nil
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fetching {
		return errKnowledgeAssetsFetching
	}
	if k.lastErr != nil && k.now().Before(k.retryAt) {
		return k.lastErr
	}

	plan, err := k.plan(k.dir)
	if err != nil {
		k.lastErr = k.describePlanFailure(err)
		k.retryAt = k.now().Add(knowledgeAssetRetry)
		return k.lastErr
	}

	k.fetching = true
	k.lastErr = nil
	k.logOnce("fetch-start", fmt.Sprintf(
		"knowledge: downloading %d embedding assets (%d MB, pinned by %s) into %s; retrieval turns on when it finishes",
		len(plan.Assets), plan.TotalBytes>>20, plan.Origin, k.dir))
	go k.download(plan)
	return errKnowledgeAssetsFetching
}

// describePlanFailure turns "no assets pinned" from a bare sentinel into the
// two things a person can actually do about it.
func (k *knowledgeAssets) describePlanFailure(err error) error {
	if errors.Is(err, assets.ErrUnsupportedPlatform) {
		msg := fmt.Sprintf(
			"knowledge: no embedding assets are pinned for this platform, so local retrieval stays off. "+
				"Supply them either by putting libonnxruntime + knowledge/model.onnx + knowledge/tokenizer.json in %s, "+
				"or by pointing %s at a manifest that pins them (%v)",
			k.dir, assets.ManifestPathEnv, err)
		k.logOnce("unsupported", msg)
		return fmt.Errorf("%w: %s", errKnowledgeAssetsUnavailable, msg)
	}
	k.logOnce("plan-error", fmt.Sprintf("knowledge: embedding assets unavailable: %v", err))
	return fmt.Errorf("%w: %w", errKnowledgeAssetsUnavailable, err)
}

// download runs the acquisition and records its outcome for the next ensure.
func (k *knowledgeAssets) download(plan assets.Plan) {
	ctx, cancel := context.WithTimeout(context.Background(), assets.DownloadTimeout)
	defer cancel()
	err := k.fetch(ctx, k.dir, plan)

	k.mu.Lock()
	defer k.mu.Unlock()
	k.fetching = false
	if err != nil {
		k.lastErr = fmt.Errorf("%w: downloading embedding assets: %w", errKnowledgeAssetsUnavailable, err)
		k.retryAt = k.now().Add(knowledgeAssetRetry)
		log.Printf("knowledge: embedding asset download failed, retrying no sooner than %s: %v", knowledgeAssetRetry, err)
		return
	}
	k.lastErr = nil
	k.retryAt = time.Time{}
	log.Printf("knowledge: embedding assets installed in %s; local retrieval is now available", k.dir)
}

// logOnce keeps one explanation per distinct reason per process, so a state
// that persists for a whole session does not narrate itself once per turn.
func (k *knowledgeAssets) logOnce(key, msg string) {
	if k.logged == nil {
		k.logged = map[string]bool{}
	}
	if k.logged[key] {
		return
	}
	k.logged[key] = true
	log.Print(msg)
}

// knowledgeDownloadDir is where a given data dir keeps downloaded assets.
func knowledgeDownloadDir(dataDir string) string {
	if dataDir == "" {
		return knowledgeDownloadDirName
	}
	return filepath.Join(dataDir, knowledgeDownloadDirName)
}
