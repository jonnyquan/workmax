//go:build desktop && cgo

// Package knowledge implements the local-first RAG foundation for the
// WorkMax desktop sidecar: MiniLM-L6-v2 sentence embeddings via ONNX Runtime.
//
// This is the L3c-1 module (see plan jolly-scribbling-blossom.md). It is the
// first cgo dependency introduced into an otherwise pure-Go sidecar
// (glebarez/modernc). The embedding model runs fully on-device; no text leaves
// the machine.
//
// Native assets (the libonnxruntime shared lib, model.onnx, tokenizer.json)
// are NOT embedded in the binary. They ship as Electron extraResources and are
// loaded by path at runtime, keeping the sidecar binary small and avoiding the
// Git-LFS-through-Go-proxy problem that breaks library-embedded models. See
// memory workmax-l3c-embedding-spike for the rationale.
package knowledge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	ort "github.com/yalue/onnxruntime_go"
	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
)

// EmbeddingDim is the dimensionality of the all-MiniLM-L6-v2 sentence
// embedding (the model's hidden / sentence_embedding output size).
const EmbeddingDim = 384

// The all-MiniLM-L6-v2 ONNX session takes three int64 inputs and returns one
// float32 output. These names must match the model's input/output names.
var (
	modelInputNames  = []string{"input_ids", "attention_mask", "token_type_ids"}
	modelOutputNames = []string{"sentence_embedding"}
)

// Embedder runs MiniLM-L6-v2 sentence embeddings through ONNX Runtime.
//
// The returned vectors are 384-dimensional and L2-normalized (the shipped
// model includes a Normalize layer, verified in the L3c-1 spike), so cosine
// similarity reduces to a dot product — relied on by the L3c-2 vector store.
//
// The underlying ORT session is safe for concurrent use; Embed and
// EmbedBatch may be called from multiple goroutines.
type Embedder struct {
	session   *ort.DynamicAdvancedSession
	tokenizer *tokenizer.Tokenizer
}

// Resources locates the native embedding assets on disk (extraResources in the
// packaged app, local paths in dev).
type Resources struct {
	// LibOnnxPath is the libonnxruntime shared library (.dylib/.so/.dll).
	LibOnnxPath string
	// ModelPath is the MiniLM model.onnx file.
	ModelPath string
	// TokenizerPath is the tokenizer.json file.
	TokenizerPath string
}

// NewEmbedder loads the ONNX Runtime shared lib, the tokenizer, and the model
// session. Close must be called to release the ORT environment.
func NewEmbedder(res Resources) (*Embedder, error) {
	for _, p := range []struct{ name, path string }{
		{"libonnxruntime", res.LibOnnxPath},
		{"model", res.ModelPath},
		{"tokenizer", res.TokenizerPath},
	} {
		if p.path == "" {
			return nil, fmt.Errorf("knowledge: %s path is empty", p.name)
		}
		if _, err := os.Stat(p.path); err != nil {
			return nil, fmt.Errorf("knowledge: %s path %q: %w", p.name, p.path, err)
		}
	}

	ort.SetSharedLibraryPath(res.LibOnnxPath)
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("knowledge: init onnxruntime (load %s): %w", res.LibOnnxPath, err)
	}

	tk, err := pretrained.FromFile(res.TokenizerPath)
	if err != nil {
		_ = ort.DestroyEnvironment()
		return nil, fmt.Errorf("knowledge: load tokenizer %s: %w", res.TokenizerPath, err)
	}

	// Dynamic session: the sequence-length axis varies per input batch, so the
	// session is created once and fed tensors shaped to each call's actual
	// token count (mirrors how the model is run upstream).
	session, err := ort.NewDynamicAdvancedSession(
		res.ModelPath, modelInputNames, modelOutputNames, nil)
	if err != nil {
		_ = ort.DestroyEnvironment()
		return nil, fmt.Errorf("knowledge: create session from %s: %w", res.ModelPath, err)
	}

	return &Embedder{session: session, tokenizer: tk}, nil
}

// Close releases the ORT session and environment. After Close the Embedder
// must not be used.
func (e *Embedder) Close() error {
	if e.session != nil {
		e.session.Destroy()
	}
	return ort.DestroyEnvironment()
}

// Dim returns the embedding dimensionality (EmbeddingDim).
func (e *Embedder) Dim() int { return EmbeddingDim }

// Embed produces a 384-dim, L2-normalized embedding for a single text.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	batch, err := e.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(batch) == 0 {
		return nil, errors.New("knowledge: empty embedding result")
	}
	return batch[0], nil
}

// EmbedBatch produces one embedding per text. Inputs are tokenized together
// (padded to the longest in the batch) and inferred in a single ORT Run.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(texts) == 0 {
		return nil, nil
	}

	inputs := make([]tokenizer.EncodeInput, 0, len(texts))
	for _, t := range texts {
		inputs = append(inputs, tokenizer.NewSingleEncodeInput(tokenizer.NewInputSequence(t)))
	}
	encodings, err := e.tokenizer.EncodeBatch(inputs, true)
	if err != nil {
		return nil, fmt.Errorf("knowledge: tokenize: %w", err)
	}
	if len(encodings) == 0 {
		return nil, errors.New("knowledge: tokenization produced no encodings")
	}

	batchSize := len(encodings)
	seqLength := len(encodings[0].Ids)
	if seqLength == 0 {
		return nil, errors.New("knowledge: zero-length token sequence")
	}

	inputShape := ort.NewShape(int64(batchSize), int64(seqLength))
	inputIDs := make([]int64, batchSize*seqLength)
	attentionMask := make([]int64, batchSize*seqLength)
	tokenTypeIDs := make([]int64, batchSize*seqLength)
	for b, enc := range encodings {
		off := b * seqLength
		for i, id := range enc.Ids {
			inputIDs[off+i] = int64(id)
		}
		for i, m := range enc.AttentionMask {
			attentionMask[off+i] = int64(m)
		}
		for i, t := range enc.TypeIds {
			tokenTypeIDs[off+i] = int64(t)
		}
	}

	inIDs, err := ort.NewTensor(inputShape, inputIDs)
	if err != nil {
		return nil, fmt.Errorf("knowledge: input_ids tensor: %w", err)
	}
	defer inIDs.Destroy()
	inMask, err := ort.NewTensor(inputShape, attentionMask)
	if err != nil {
		return nil, fmt.Errorf("knowledge: attention_mask tensor: %w", err)
	}
	defer inMask.Destroy()
	inTypes, err := ort.NewTensor(inputShape, tokenTypeIDs)
	if err != nil {
		return nil, fmt.Errorf("knowledge: token_type_ids tensor: %w", err)
	}
	defer inTypes.Destroy()

	outShape := ort.NewShape(int64(batchSize), EmbeddingDim)
	out, err := ort.NewEmptyTensor[float32](outShape)
	if err != nil {
		return nil, fmt.Errorf("knowledge: output tensor: %w", err)
	}
	defer out.Destroy()

	if err := e.session.Run([]ort.Value{inIDs, inMask, inTypes}, []ort.Value{out}); err != nil {
		return nil, fmt.Errorf("knowledge: inference: %w", err)
	}

	flat := out.GetData()
	if got := len(flat); got != batchSize*EmbeddingDim {
		return nil, fmt.Errorf("knowledge: unexpected output size %d, want %d", got, batchSize*EmbeddingDim)
	}

	results := make([][]float32, batchSize)
	for i := range batchSize {
		vec := make([]float32, EmbeddingDim)
		copy(vec, flat[i*EmbeddingDim:(i+1)*EmbeddingDim])
		results[i] = vec
	}
	return results, nil
}

// libOnnxName returns the platform-conventional libonnxruntime filename.
func libOnnxName() string {
	switch runtime.GOOS {
	case "windows":
		return "onnxruntime.dll"
	case "darwin":
		return "libonnxruntime.dylib"
	default:
		return "libonnxruntime.so"
	}
}

// ResolveResources resolves the native embedding assets from explicit env vars
// first, then from a resources directory (WORKMAX_RESOURCES_DIR, default
// "resources" relative to the process working dir). Conventional layout under
// the resources dir:
//
//	<res>/libonnxruntime.{dylib|so|dll}
//	<res>/knowledge/model.onnx
//	<res>/knowledge/tokenizer.json
//
// This lets the packaged sidecar point at its bundled resources folder via a
// single env var, while dev/tests can override any field individually.
func ResolveResources() (Resources, error) {
	return ResolveResourcesIn("")
}

// ResolveResourcesIn is ResolveResources with an explicit default resources
// directory, for callers that know where their assets are without being told
// through the environment — notably the in-process Wails boot, which derives
// the path from the executable location (Contents/Resources on macOS) because
// there is no child process to hand an env var to.
//
// Precedence, most specific first: the individual *_PATH env vars, then
// WORKMAX_RESOURCES_DIR, then dir, then "resources" relative to the working
// directory. The env vars stay ahead of dir so a developer can still point a
// packaged build at a local checkout of the assets.
func ResolveResourcesIn(dir string) (Resources, error) {
	var res Resources

	if v := os.Getenv("ONNXRUNTIME_LIB_PATH"); v != "" {
		res.LibOnnxPath = v
	}
	if v := os.Getenv("WORKMAX_KNOWLEDGE_MODEL"); v != "" {
		res.ModelPath = v
	}
	if v := os.Getenv("WORKMAX_KNOWLEDGE_TOKENIZER"); v != "" {
		res.TokenizerPath = v
	}

	// Fill anything still unset from the resources dir.
	resDir := os.Getenv("WORKMAX_RESOURCES_DIR")
	if resDir == "" {
		resDir = dir
	}
	if resDir == "" {
		resDir = "resources"
	}
	if res.LibOnnxPath == "" {
		res.LibOnnxPath = filepath.Join(resDir, libOnnxName())
	}
	if res.ModelPath == "" {
		res.ModelPath = filepath.Join(resDir, "knowledge", "model.onnx")
	}
	if res.TokenizerPath == "" {
		res.TokenizerPath = filepath.Join(resDir, "knowledge", "tokenizer.json")
	}

	missing := make([]string, 0, 3)
	for _, p := range []struct{ name, path string }{
		{"libonnxruntime", res.LibOnnxPath},
		{"model", res.ModelPath},
		{"tokenizer", res.TokenizerPath},
	} {
		if _, err := os.Stat(p.path); err != nil {
			missing = append(missing, fmt.Sprintf("%s (%s)", p.name, p.path))
		}
	}
	if len(missing) > 0 {
		return res, fmt.Errorf("knowledge: missing native resources: %v (set WORKMAX_RESOURCES_DIR or the individual *_PATH env vars)", missing)
	}
	return res, nil
}
