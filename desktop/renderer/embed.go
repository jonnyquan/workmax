// Package renderer carries the bundled desktop renderer inside the binary.
//
// It used to be served with os.DirFS from a directory next to the executable.
// That works, and it is why every packaged build still ships the same files in
// Contents/Resources — but a directory is writable by whoever can write to the
// installation, so "the renderer is exactly the files we shipped" stopped being
// true the moment the .app was on disk. The webview runs that code with the
// capability path in its URL and a proxy that injects the local token on its
// behalf, so an attacker who can swap renderer.js has the sidecar. Embedding
// makes the answer structural: the bytes are in the executable, and the
// executable is what code signing covers.
//
// The file list below is an allowlist, deliberately spelled out rather than
// globbed, and it must agree with the three other places that name the same
// five files:
//
//	desktop/scripts/check-bundled-renderer.sh   (source allowlist)
//	desktop/scripts/build-mac.sh                (RENDERER_FILES)
//	desktop/scripts/inspect-mac-package.sh      (bundle allowlist)
//
// check-bundled-renderer.sh reads this list back and fails if it and the source
// allowlist have drifted, so adding a file here without adding it there — or
// the reverse — is a build failure rather than a shipped surprise.
package renderer

import (
	"embed"
	"io/fs"
)

//go:embed en/desktop/index.html
//go:embed en/desktop/styles.css
//go:embed en/desktop/renderer.js
//go:embed en/desktop/shim.js
//go:embed en/desktop/lib/desktop-bridge.js
var bundled embed.FS

// FS returns the renderer rooted where index.html is, which is the shape the
// UI server's file handler wants: it serves "/" from the root of what it is
// given.
func FS() (fs.FS, error) {
	return fs.Sub(bundled, "en/desktop")
}
