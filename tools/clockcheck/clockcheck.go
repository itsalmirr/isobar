// Package clockcheck flags direct time.Now / time.Since / time.Until
// calls outside allowed packages, enforcing the time-source discipline
// adopted in ADR-0012.
//
// Policy: all wall-time access in internal/* flows through clock.Clock
// or a `now func() time.Time` struct field. Direct time.Now (and its
// composites time.Since / time.Until) is reserved for the constructed
// production seam — never in business logic — and is therefore allowed
// only in:
//
//  1. internal/clock itself (the seam's package)
//  2. cmd/*/main.go entry points (where Wall() is built and threaded down)
//  3. *_test.go files (test-only setup)
//  4. internal/journal/** — temporarily exempted; clock migration is a
//     follow-up to A4 to avoid colliding with parallel A1+A2 work on
//     this package. Remove this exemption when journal is migrated.
//  5. Lines or declarations marked //nolint:clockcheck
//
// Run as part of `make lint`; failing the build is the enforcement
// mechanism. To suppress a single line, add a leading //nolint:clockcheck
// comment justifying why the direct read is necessary.
package clockcheck

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer is the singleton x/tools analysis.Analyzer for the clockcheck
// rule. Wire it into singlechecker or multichecker; see
// tools/clockcheck/main.go for the production entry point.
var Analyzer = &analysis.Analyzer{
	Name:     "clockcheck",
	Doc:      "report direct time.Now / time.Since / time.Until calls outside the ADR-0012 allowlist",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

// bannedNames is the set of identifiers on the standard `time` package
// that read wall time directly. These are the names callers must not use
// outside the allowlist.
//
// Excluded by design:
//
//   - time.NewTicker, time.NewTimer, time.After, time.AfterFunc,
//     time.Sleep — these schedule events relative to monotonic-anchored
//     time and don't expose the current wall clock as a value.
//   - time.Tick — same; documented elsewhere as resource-leaky but not
//     a clock-discipline issue.
var bannedNames = map[string]struct{}{
	"Now":   {},
	"Since": {},
	"Until": {},
}

func run(pass *analysis.Pass) (interface{}, error) {
	if pkgIsExempt(pass.Pkg.Path()) {
		return nil, nil
	}

	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	fset := pass.Fset

	// Build a per-file map of //nolint:clockcheck-tagged line numbers for
	// fast lookup during the call-expression walk. Comments are file-level
	// so this is unavoidably O(files × comments).
	nolints := collectNolints(pass.Files, fset)

	insp.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node) {
		call, _ := n.(*ast.CallExpr)
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		// Match the call's receiver to the standard `time` package via
		// type info — a local var named `time` would not match. This is
		// the same idea x/tools' other analyzers use.
		if !isStdTimePkg(pass, sel.X) {
			return
		}
		if _, banned := bannedNames[sel.Sel.Name]; !banned {
			return
		}

		pos := fset.Position(call.Pos())
		// Skip *_test.go files. The position's Filename is the on-disk
		// path; HasSuffix is sufficient because Go's filename rule for
		// build-time test files is filename-based.
		if strings.HasSuffix(pos.Filename, "_test.go") {
			return
		}
		// Skip cmd/*/main.go entry points. We match on the absolute
		// path's contents to keep the rule robust against repo layout
		// changes (e.g., a future cmd/internal subfolder).
		if isCmdMain(pos.Filename) {
			return
		}
		if isNolintLine(nolints, pos.Filename, pos.Line) {
			return
		}

		pass.Reportf(call.Pos(),
			"time.%s is restricted; per ADR-0012 use clock.Clock injection, "+
				"a `now func() time.Time` field, or clock.Now (for envelopes). "+
				"Add //nolint:clockcheck with justification if direct access is unavoidable.",
			sel.Sel.Name)
	})
	return nil, nil
}

// pkgIsExempt returns true for packages where the analyzer is silent.
// Keep this list short — every entry is debt we have to pay back later.
func pkgIsExempt(pkgPath string) bool {
	switch {
	case pkgPath == "github.com/wh0o0pz/isobar/internal/clock":
		return true
	case strings.HasPrefix(pkgPath, "github.com/wh0o0pz/isobar/internal/journal"):
		// TODO(ADR-0012): journal clock migration is a follow-up PR.
		// Parallel A1+A2 work is rewriting journal.go; remove this
		// branch once that PR merges and journal is migrated to the
		// now-func field pattern.
		return true
	}
	return false
}

// isStdTimePkg returns true when expr is an identifier resolving to the
// standard `time` package. Robust against local variables shadowing the
// name `time`.
func isStdTimePkg(pass *analysis.Pass, expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	obj, ok := pass.TypesInfo.Uses[id].(*types.PkgName)
	if !ok {
		return false
	}
	return obj.Imported().Path() == "time"
}

// isCmdMain returns true for files at cmd/<binary>/main.go (the standard
// Go entry-point layout). The check is path-suffix-based so it works
// regardless of where the project lives on disk.
func isCmdMain(filename string) bool {
	// Normalize separators for cross-OS robustness; Go test fixtures and
	// `go vet` emit native paths.
	normalized := filename
	if sep := byte('\\'); strings.IndexByte(normalized, sep) >= 0 {
		normalized = strings.ReplaceAll(normalized, "\\", "/")
	}
	// Match …/cmd/<name>/main.go where <name> is any non-empty segment.
	// We require the literal "/cmd/" prefix and a "/main.go" suffix with
	// exactly one path segment between them.
	const cmdPrefix = "/cmd/"
	const mainSuffix = "/main.go"
	i := strings.LastIndex(normalized, cmdPrefix)
	if i < 0 {
		return false
	}
	rest := normalized[i+len(cmdPrefix):]
	if !strings.HasSuffix(rest, mainSuffix) {
		return false
	}
	mid := rest[:len(rest)-len(mainSuffix)]
	// Exactly one segment between cmd/ and main.go.
	return mid != "" && !strings.Contains(mid, "/")
}

// nolintKey identifies a (file, line) where //nolint:clockcheck applies.
type nolintKey struct {
	file string
	line int
}

// collectNolints walks every comment in the inspected files and records
// the (file, line) of each line that has a //nolint:clockcheck (or a
// bare //nolint with no rule list — golangci-lint convention).
//
// We accept the same comment on the line above the violation, since
// that matches the staticcheck / nolintlint convention. (Inline trailing
// //nolint comments are also picked up because their token.Position line
// equals the call's line.)
func collectNolints(files []*ast.File, fset *token.FileSet) map[nolintKey]struct{} {
	out := map[nolintKey]struct{}{}
	for _, f := range files {
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if !commentMatchesNolint(c.Text) {
					continue
				}
				pos := fset.Position(c.Slash)
				// Cover both the line the comment lives on and the next
				// line — handles "//nolint:clockcheck" on its own line
				// above the offending call (the golangci convention).
				out[nolintKey{file: pos.Filename, line: pos.Line}] = struct{}{}
				out[nolintKey{file: pos.Filename, line: pos.Line + 1}] = struct{}{}
			}
		}
	}
	return out
}

// commentMatchesNolint reports whether text is a //nolint or
// //nolint:clockcheck-bearing comment. Matches the golangci-lint
// shapes: "//nolint", "//nolint:clockcheck",
// "//nolint:clockcheck,gosec", "//nolint:foo,clockcheck".
//
// A space between "//" and "nolint" is rejected — golangci-lint requires
// "//nolint" with no leading space, and we follow that convention to
// avoid accidentally honoring stray prose like "// nolint, sure...".
func commentMatchesNolint(text string) bool {
	// Block-comment form is uncommon for nolint but accept it for parity.
	if rest, ok := strings.CutPrefix(text, "/*"); ok {
		text = "//" + rest
	}
	// Require the literal "//nolint" prefix — no space allowed between
	// the slashes and the keyword.
	s, ok := strings.CutPrefix(text, "//nolint")
	if !ok {
		return false
	}
	// Bare //nolint with no list (or with a trailing space then prose) —
	// broad suppression.
	if s == "" || strings.HasPrefix(s, " ") || strings.HasPrefix(s, "\t") {
		return true
	}
	if !strings.HasPrefix(s, ":") {
		return false
	}
	// Inspect each comma-separated linter name.
	list := s[1:]
	// Stop at whitespace if any (golangci allows "//nolint:foo // explanation").
	if idx := strings.IndexAny(list, " \t"); idx >= 0 {
		list = list[:idx]
	}
	for _, name := range strings.Split(list, ",") {
		if strings.TrimSpace(name) == "clockcheck" {
			return true
		}
	}
	return false
}

// isNolintLine returns true if a //nolint:clockcheck comment is registered
// for the file/line pair (or the line immediately above, per collectNolints).
func isNolintLine(nolints map[nolintKey]struct{}, filename string, line int) bool {
	_, ok := nolints[nolintKey{file: filename, line: line}]
	return ok
}
