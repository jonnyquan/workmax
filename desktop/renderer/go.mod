// The bundled renderer is its own module for one reason: //go:embed cannot
// reach outside the directory tree of the package that declares it, and the
// shell's module root is desktop/wails. Rather than copy the renderer into the
// shell's tree at build time — a second copy, drifting from the first — the
// files stay where they are and a three-line package here hands them to the
// shell as an fs.FS.
//
// It has no dependencies and never will: embedding is all it does.
module workmax/desktop/renderer

go 1.25.0
