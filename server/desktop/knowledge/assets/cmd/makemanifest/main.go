//go:build desktop

// makemanifest turns a directory of local embedding assets into a pinned
// manifest for first-launch download.
//
//	go run ./server/desktop/knowledge/assets/cmd/makemanifest \
//	  -dir ~/workmax-spike/resources \
//	  -base-url https://downloads.workmax.app/knowledge/v1 \
//	  -platform darwin/arm64
//
// The output is a complete manifest.json: hosting is the release process's
// half (upload the three files under base-url, unmodified), this is the other
// half — the sizes and SHA-256 digests the downloader refuses to run without.
// Before printing, the generated manifest is round-tripped through PlanFor,
// so a manifest that leaves this tool is one the client has already accepted.
//
// Base URL naming: everything under one versioned directory lets a model swap
// ship as a new directory rather than an in-place overwrite, which matters
// because a URL's content may be cached anywhere between us and the user —
// the digest pinning assumes a URL's bytes never change.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"server/desktop/knowledge/assets"
)

func main() {
	var (
		dir      = flag.String("dir", "", "directory with the assets laid out the way ResolveResourcesIn expects")
		baseURL  = flag.String("base-url", "", "URL prefix the assets will be hosted under; must not end in /")
		platform = flag.String("platform", runtime.GOOS+"/"+runtime.GOARCH, "os/arch key the assets are pinned for")
		out      = flag.String("out", "", "write here instead of stdout")
	)
	flag.Parse()
	log.SetFlags(0)
	if err := run(*dir, *baseURL, *platform, *out); err != nil {
		log.Fatalf("makemanifest: %v", err)
	}
}

// assetSpec is one file of the conventional layout, in the order a downloader
// should fetch it: the shared library first, because nothing else is usable
// without it.
type assetSpec struct {
	name  string
	path  string // relative to the resources dir, and to baseURL when hosted
	label string
	// executable marks the shared library, the one file that needs its exec
	// bit after download.
	executable bool
}

func specsFor(platform string) ([]assetSpec, error) {
	lib := ""
	switch platform {
	case "darwin/arm64", "darwin/amd64":
		lib = "libonnxruntime.dylib"
	case "linux/amd64", "linux/arm64":
		lib = "libonnxruntime.so"
	case "windows/amd64":
		lib = "onnxruntime.dll"
	default:
		return nil, fmt.Errorf("unknown platform key %q", platform)
	}
	return []assetSpec{
		{name: "ONNX Runtime library", path: lib, label: lib, executable: true},
		{name: "embedding model", path: filepath.Join("knowledge", "model.onnx"), label: "model"},
		{name: "tokenizer", path: filepath.Join("knowledge", "tokenizer.json"), label: "tokenizer"},
	}, nil
}

func run(dir, baseURL, platform, out string) error {
	if dir == "" {
		return errors.New("-dir is required")
	}
	if baseURL == "" {
		return errors.New("-base-url is required (the URL prefix the assets will be hosted under)")
	}
	if baseURL[len(baseURL)-1] == '/' {
		return errors.New("-base-url must not end in /")
	}

	specs, err := specsFor(platform)
	if err != nil {
		return err
	}

	// The manifest struct lives in the parent package's private types, so the
	// output is assembled with the same field names the JSON carries. The
	// PlanFor round-trip below is what proves the shapes agree.
	type asset struct {
		Name       string `json:"name"`
		Path       string `json:"path"`
		URL        string `json:"url"`
		SHA256     string `json:"sha256"`
		Size       int64  `json:"size"`
		Executable bool   `json:"executable,omitempty"`
	}
	list := make([]asset, 0, len(specs))
	for _, s := range specs {
		local := filepath.Join(dir, filepath.FromSlash(s.path))
		h, size, err := hashFile(local)
		if err != nil {
			return fmt.Errorf("%s (%s): %w", s.name, local, err)
		}
		log.Printf("%-10s %8d bytes  %s  %s", s.label, size, h[:16]+"…", s.path)
		list = append(list, asset{
			Name:       s.name,
			Path:       s.path,
			URL:        baseURL + "/" + s.path,
			SHA256:     h,
			Size:       size,
			Executable: s.executable,
		})
	}

	m := map[string]any{
		"schemaVersion": 1,
		"platforms":     map[string][]asset{platform: list},
	}
	blob, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}

	// Round-trip: write the manifest into its own temp directory and resolve
	// it the way a client would (<dir>/manifest.json, no env override). A
	// manifest this tool emits must already be one the downloader accepts —
	// a typo in a field name would otherwise surface as a silently empty
	// platform at download time, on a user's machine.
	mdir, err := os.MkdirTemp("", "makemanifest-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mdir)
	if err := os.WriteFile(filepath.Join(mdir, assets.ManifestFileName), blob, 0o644); err != nil {
		return err
	}
	if _, err := assets.PlanForPlatform(mdir, platform); err != nil {
		return fmt.Errorf("generated manifest failed validation: %w", err)
	}

	if out == "" {
		_, err := os.Stdout.Write(append(blob, '\n'))
		return err
	}
	return os.WriteFile(out, append(blob, '\n'), 0o644)
}

// hashFile streams a file through SHA-256 and reports its digest and size.
// The assets are tens of megabytes; reading them whole would work today and
// waste a hundred megabytes the day the model grows.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}
