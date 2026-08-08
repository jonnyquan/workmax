package workagent

// idor_contract_test.go — IDOR posture continuous invariant
// (2026-05-17). Pins the §3.3 platform-state.md claim that every
// owner-scoped HTTP handler in this package routes its id-shaped
// path/query parameter through a uid-scoped repo lookup.
//
// Why this test exists: the 2026-05-17 audit (see commit + audit
// report in the conversation thread) verified the claim holds for
// all 22 then-extant handlers. Without a continuous check, the
// next "I added a new endpoint" PR could silently re-introduce
// IDOR. Code review alone is not enough — the dangerous shape
// (a handler reading c.Param("id") and feeding it straight into
// a non-ForOwner repo method) is easy to miss in diff review.
//
// Mechanism: AST-walk every non-test .go file in this package.
// For each function taking `*gin.Context`:
//   1. If the function body contains the `IDOR-EXEMPT-FUNC:`
//      marker comment (anywhere — checked via string contains on
//      the function's source), skip.
//   2. If the *file* contains the `IDOR-EXEMPT-FILE:` marker (in
//      any comment block), skip the whole file. This is for files
//      whose entire purpose is public-by-design (e.g.
//      conversation_share.go).
//   3. Otherwise, if the function reads c.Param / c.Query for an
//      id-shaped name (id, uuid, threadId, jobId, messageId, etc.),
//      require the function body ALSO contains either `ForOwner`
//      or `GetUserID(` (the two known-safe shapes).
//
// Granted: the second leg is a heuristic — a malicious-or-careless
// handler could call GetUserID and then ignore the result. But the
// real risk this defends against is the "forgot to scope it"
// regression, not deliberate sabotage. Code review catches the
// latter; this test catches the former.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// idShapedParamKeys is the set of c.Param / c.Query argument
// strings that name a specific user-owned resource. Adding a new
// id-shaped name (e.g. "projectId") requires extending this list
// AND ensuring every handler that reads it is uid-scoped.
var idShapedParamKeys = map[string]bool{
	"id":        true,
	"uuid":      true,
	"threadId":  true,
	"thread_id": true,
	"jobId":     true,
	"job_id":    true,
	"messageId": true,
	"fileId":    true,
	"assetId":   true,
}

// safeRepoPatterns are the substrings that, if found anywhere in
// the function body, indicate the handler is uid-scoping its
// lookup. Two-leg defence: either the explicit *ForOwner repo
// suffix, OR the GetUserID call paired with one of the known
// owner-aware service calls.
//
// We deliberately accept `GetUserID(` as a safe signal because
// every audited handler that uses it does so as the input to a
// uid-aware repo/service. A future handler that calls GetUserID
// but throws away the result would slip through this check — code
// review is the second leg of defence.
var safeRepoPatterns = []string{
	"ForOwner",
	"GetUserID(",
}

// exemptFileMarker — placing this string anywhere in a file's
// comments excludes the whole file from the scan. Use for files
// whose every handler is public-by-design.
const exemptFileMarker = "IDOR-EXEMPT-FILE"

// exemptFuncMarker — placing this string anywhere in a function's
// preceding comment OR body excludes that single function from
// the scan. Use sparingly — every exemption is a security
// surface area the reviewer must vet.
const exemptFuncMarker = "IDOR-EXEMPT-FUNC"

// TestIDORContract_EveryGinHandlerScopesByUID is the headline
// invariant. Walks the package source, classifies each
// gin.Context-taking function, and asserts the uid-scoping
// requirement holds.
func TestIDORContract_EveryGinHandlerScopesByUID(t *testing.T) {
	files := findPackageGoFiles(t, ".")
	if len(files) == 0 {
		t.Fatal("found zero .go files in package — test logic broken (path resolution?)")
	}

	scanned := 0
	exemptedFiles := 0
	exemptedFuncs := 0
	for _, path := range files {
		srcBytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(srcBytes)

		if strings.Contains(src, exemptFileMarker) {
			exemptedFiles++
			continue
		}

		fset := token.NewFileSet()
		fileAST, err := parser.ParseFile(fset, path, srcBytes, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		// Walk BOTH FuncDecl (top-level handlers) AND FuncLit
		// (closure handlers returned by gin.HandlerFunc factories
		// like asset_library_api.go's handleGetAsset). The
		// FuncDecl pass catches the bulk; the FuncLit pass closes
		// the closure-handler gap that the original audit
		// surfaced (4 handlers in asset_library_api.go).
		ast.Inspect(fileAST, func(n ast.Node) bool {
			var (
				body     *ast.BlockStmt
				params   *ast.FieldList
				name     string
				startPos token.Pos
				endPos   token.Pos
			)
			switch v := n.(type) {
			case *ast.FuncDecl:
				if v.Body == nil {
					return true
				}
				body = v.Body
				params = v.Type.Params
				name = v.Name.Name
				// Start at the doc comment if present so a
				// `// IDOR-EXEMPT-FUNC:` marker above the function
				// (Go convention for documenting public-ish API)
				// is included in the substring scan.
				startPos = v.Pos()
				if v.Doc != nil {
					startPos = v.Doc.Pos()
				}
				endPos = v.End()
			case *ast.FuncLit:
				body = v.Body
				params = v.Type.Params
				name = "<closure>"
				startPos = v.Pos()
				endPos = v.End()
			default:
				return true
			}
			if !isGinContextParam(params) {
				return true
			}

			// Extract function source so we can string-grep for
			// safe patterns. start/end positions on Body include
			// the braces; that's fine for substring checks.
			startOff := fset.Position(startPos).Offset
			endOff := fset.Position(endPos).Offset
			if startOff < 0 || endOff > len(src) || startOff >= endOff {
				return true
			}
			fnSrc := src[startOff:endOff]

			if strings.Contains(fnSrc, exemptFuncMarker) {
				exemptedFuncs++
				return true
			}

			paramKey, paramFound := findIDShapedParamRead(fnSrc)
			if !paramFound {
				return true
			}

			scanned++
			if !anyOf(fnSrc, safeRepoPatterns) {
				t.Errorf(
					"\n  IDOR contract violation in %s\n"+
						"  Handler %s reads c.Param/Query(%q) but its body contains neither\n"+
						"  `ForOwner` nor `GetUserID(`.\n"+
						"  Either:\n"+
						"    (a) route the lookup through the *ForOwner repo pattern, or\n"+
						"    (b) if this is intentional (e.g. public share endpoint), add the\n"+
						"        marker comment `// %s: <rationale>` inside the function body\n"+
						"        AND ensure the auth model is documented.\n",
					relPathOrAbs(path), name, paramKey, exemptFuncMarker,
				)
			}
			// Returning true lets ast.Inspect descend into nested
			// closures (closure-inside-closure is theoretical but
			// not pathological — we want to scan it).
			_ = body
			return true
		})
	}

	if scanned == 0 {
		t.Fatal("scanned zero id-shaped handlers — either the scope shrank or the scan logic broke")
	}
	t.Logf("IDOR contract: scanned=%d id-shaped handlers, exempted_files=%d, exempted_funcs=%d",
		scanned, exemptedFiles, exemptedFuncs)
}

// TestIDORContract_SelfTestMethodCorrectness pins that the scan
// helpers correctly classify the two known shapes — one known-good
// handler (LoadByIDForOwner pattern) and one known-exempt handler
// (the DeleteMessage two-step pattern). If either flip, the main
// test could pass or fail for the wrong reason.
//
// Per the team's "regex sweeps must self-test" rule from memory.
func TestIDORContract_SelfTestMethodCorrectness(t *testing.T) {
	tests := []struct {
		name        string
		src         string
		wantParam   string
		wantParamOK bool
		wantSafe    bool
	}{
		{
			name: "known-good: ForOwner shape",
			src: `func (api *X) DeleteConversation(c *gin.Context) {
				uid := utils.GetUserID(c)
				threadId := c.Param("id")
				_ = workagentService.DefaultThreadRepository().LoadByIDForOwner(uid, ...)
			}`,
			wantParam:   "id",
			wantParamOK: true,
			wantSafe:    true,
		},
		{
			name: "dangerous: no uid scoping",
			src: `func (api *X) UnsafeHandler(c *gin.Context) {
				threadId := c.Param("id")
				_ = workagentService.LookupByID(threadId)
			}`,
			wantParam:   "id",
			wantParamOK: true,
			wantSafe:    false,
		},
		{
			name: "non-id param: q for search query",
			src: `func (api *X) Search(c *gin.Context) {
				q := c.Query("q")
				_ = search(q)
			}`,
			wantParamOK: false,
		},
		{
			name: "GetUserID-only shape",
			src: `func (api *X) ListMine(c *gin.Context) {
				uid := utils.GetUserID(c)
				id := c.Param("id")
				_ = svc.ListForUser(uid, id)
			}`,
			wantParam:   "id",
			wantParamOK: true,
			wantSafe:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotKey, gotOK := findIDShapedParamRead(tt.src)
			if gotOK != tt.wantParamOK {
				t.Errorf("findIDShapedParamRead ok = %v, want %v", gotOK, tt.wantParamOK)
			}
			if tt.wantParamOK && gotKey != tt.wantParam {
				t.Errorf("findIDShapedParamRead key = %q, want %q", gotKey, tt.wantParam)
			}
			if !tt.wantParamOK {
				return
			}
			gotSafe := anyOf(tt.src, safeRepoPatterns)
			if gotSafe != tt.wantSafe {
				t.Errorf("anyOf safe = %v, want %v", gotSafe, tt.wantSafe)
			}
		})
	}
}

// findPackageGoFiles enumerates non-test .go files in the given
// directory. Excludes _test.go since the contract enforces
// production code; tests can legitimately reach past the API
// boundary for setup.
func findPackageGoFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// isGinContextParam returns true if the parameter list is exactly
// one `*gin.Context`. Works for both FuncDecl and FuncLit since
// both expose a *ast.FieldList. Receiver presence is irrelevant
// — handlers can be method, free function, or closure.
func isGinContextParam(params *ast.FieldList) bool {
	if params == nil || len(params.List) != 1 {
		return false
	}
	p := params.List[0]
	star, ok := p.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "gin" && sel.Sel.Name == "Context"
}

// findIDShapedParamRead scans the source for c.Param("...") /
// c.Query("...") calls whose argument string is in
// idShapedParamKeys. Returns the first match. Substring-based
// rather than AST-based because (a) the AST walk is already in
// the caller and (b) we only need to know IF an id-shaped param
// is read, not where.
func findIDShapedParamRead(src string) (string, bool) {
	for key := range idShapedParamKeys {
		needles := []string{
			"c.Param(\"" + key + "\")",
			"c.Query(\"" + key + "\")",
			"c.PostForm(\"" + key + "\")",
		}
		for _, n := range needles {
			if strings.Contains(src, n) {
				return key, true
			}
		}
	}
	return "", false
}

// anyOf reports whether s contains any of the needles. Tiny
// helper; inlined into the main test would obscure the intent.
func anyOf(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// relPathOrAbs returns the path relative to cwd if possible,
// otherwise the path as-is. Keeps test output readable when run
// from `cd server && go test ./...`.
func relPathOrAbs(p string) string {
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, p); err == nil {
			return rel
		}
	}
	return p
}
