//go:build desktop && cgo

package knowledge

import (
	"context"
	"math"
	"testing"
)

// TestEmbedder is the L3c-1 integration test: it exercises the real onnxruntime
// + MiniLM path through the production Embedder API. It auto-skips when the
// native resources (libonnxruntime, model.onnx, tokenizer.json) are not
// resolvable, so `go test ./...` in CI without the ~120MB of assets still
// passes. Run locally with the resources assembled, e.g.:
//
//	WORKMAX_RESOURCES_DIR=~/workmax-spike/resources \
//	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go test ./desktop/knowledge/
func TestEmbedder(t *testing.T) {
	res, err := ResolveResources()
	if err != nil {
		t.Skipf("native embedding resources unavailable: %v", err)
	}

	emb, err := NewEmbedder(res)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	defer emb.Close()

	if got := emb.Dim(); got != EmbeddingDim {
		t.Fatalf("Dim() = %d, want %d", got, EmbeddingDim)
	}

	const (
		anchor   = "A man is playing guitar on a stage."
		similar  = "Someone performs music with a guitar in front of an audience."
		unrelated = "The stock market crashed after the earnings report."
	)

	batch, err := emb.EmbedBatch(context.Background(), []string{anchor, similar, unrelated})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(batch) != 3 {
		t.Fatalf("EmbedBatch returned %d vectors, want 3", len(batch))
	}
	for i, v := range batch {
		if len(v) != EmbeddingDim {
			t.Fatalf("vector %d has dim %d, want %d", i, len(v), EmbeddingDim)
		}
		// The shipped model normalizes output; guard the invariant the L3c-2
		// vector store relies on (cosine == dot product).
		if n := l2(v); math.Abs(n-1.0) > 1e-3 {
			t.Errorf("vector %d not unit-norm: |v|=%.4f", i, n)
		}
	}

	// Single Embed() must agree with the batch result for the same input.
	single, err := emb.Embed(context.Background(), anchor)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(single) != EmbeddingDim {
		t.Fatalf("Embed dim = %d, want %d", len(single), EmbeddingDim)
	}
	if c := cosine(single, batch[0]); c < 0.999 {
		t.Fatalf("Embed vs EmbedBatch inconsistent for anchor: cos=%.4f", c)
	}

	cosSim, cosUnrel := cosine(batch[0], batch[1]), cosine(batch[0], batch[2])
	t.Logf("cos(anchor,similar)=%.4f cos(anchor,unrelated)=%.4f |anchor|=%.4f",
		cosSim, cosUnrel, l2(batch[0]))
	if !(cosSim > cosUnrel) {
		t.Fatalf("semantic check failed: similar (%.4f) must beat unrelated (%.4f)", cosSim, cosUnrel)
	}

	// Empty input is a no-op, not an error.
	if got, err := emb.EmbedBatch(context.Background(), nil); err != nil || len(got) != 0 {
		t.Fatalf("EmbedBatch(nil) = %v, %v; want ([], nil)", got, err)
	}
}

// TestEmbedCancelledContext verifies the ctx guard returns before hitting ORT.
func TestEmbedCancelledContext(t *testing.T) {
	res, err := ResolveResources()
	if err != nil {
		t.Skipf("native embedding resources unavailable: %v", err)
	}
	emb, err := NewEmbedder(res)
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	defer emb.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := emb.Embed(ctx, "anything"); err == nil {
		t.Fatal("Embed with cancelled context: want error, got nil")
	}
}

// TestResolveResourcesEnvPrecedence checks that explicit env vars override the
// resources-dir layout. Does not require onnxruntime.
func TestResolveResourcesEnvPrecedence(t *testing.T) {
	t.Setenv("WORKMAX_RESOURCES_DIR", t.TempDir()) // intentionally has no files
	t.Setenv("ONNXRUNTIME_LIB_PATH", "/tmp/libonnx.dylib")
	t.Setenv("WORKMAX_KNOWLEDGE_MODEL", "/tmp/model.onnx")
	t.Setenv("WORKMAX_KNOWLEDGE_TOKENIZER", "/tmp/tokenizer.json")

	res, err := ResolveResources()
	if err == nil {
		t.Fatal("expected missing-resource error for nonexistent paths")
	}
	if res.LibOnnxPath != "/tmp/libonnx.dylib" ||
		res.ModelPath != "/tmp/model.onnx" ||
		res.TokenizerPath != "/tmp/tokenizer.json" {
		t.Fatalf("env vars not honored: %+v", res)
	}
}

func l2(v []float32) float64 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	return math.Sqrt(s)
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	na, nb := l2(a), l2(b)
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (na * nb)
}
