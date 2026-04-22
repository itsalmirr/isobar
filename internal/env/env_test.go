package env

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	require.NoError(t, Load(filepath.Join(t.TempDir(), "nope.env")))
}

func TestLoadBasicKeyValues(t *testing.T) {
	t.Setenv("ISOBAR_TEST_BASIC", "")
	path := writeEnv(t, `
# comment
FOO=bar
ISOBAR_TEST_BASIC=hello
export BAZ=qux
`)
	require.NoError(t, Load(path))
	require.Equal(t, "hello", os.Getenv("ISOBAR_TEST_BASIC"))
}

func TestLoadDoesNotOverwriteExisting(t *testing.T) {
	t.Setenv("ISOBAR_TEST_EXISTING", "original")
	path := writeEnv(t, `ISOBAR_TEST_EXISTING=new`)
	require.NoError(t, Load(path))
	require.Equal(t, "original", os.Getenv("ISOBAR_TEST_EXISTING"))
}

func TestLoadQuotedAndMultiline(t *testing.T) {
	t.Setenv("ISOBAR_TEST_QUOTED", "")
	t.Setenv("ISOBAR_TEST_MULTILINE", "")
	path := writeEnv(t, "ISOBAR_TEST_QUOTED=\"hello world\"\nISOBAR_TEST_MULTILINE=\"line1\nline2\nline3\"\n")
	require.NoError(t, Load(path))
	require.Equal(t, "hello world", os.Getenv("ISOBAR_TEST_QUOTED"))
	require.Equal(t, "line1\nline2\nline3", os.Getenv("ISOBAR_TEST_MULTILINE"))
}

func TestLoadIgnoresBlankAndComments(t *testing.T) {
	t.Setenv("ISOBAR_TEST_IGNORE", "")
	path := writeEnv(t, `
# just a comment

   # indented comment
ISOBAR_TEST_IGNORE=keep
`)
	require.NoError(t, Load(path))
	require.Equal(t, "keep", os.Getenv("ISOBAR_TEST_IGNORE"))
}

func TestLoadNearestWalksUp(t *testing.T) {
	t.Setenv("ISOBAR_TEST_NEAREST", "")

	top := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(top, ".env"),
		[]byte("ISOBAR_TEST_NEAREST=found\n"), 0o600))

	sub := filepath.Join(top, "a", "b", "c")
	require.NoError(t, os.MkdirAll(sub, 0o750))

	path, err := LoadNearest(sub)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(top, ".env"), path)
	require.Equal(t, "found", os.Getenv("ISOBAR_TEST_NEAREST"))
}

func TestLoadNearestNoFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path, err := LoadNearest(dir)
	require.NoError(t, err)
	require.Equal(t, "", path)
}

func TestParseHandlesEmptyValue(t *testing.T) {
	out := parse("EMPTY=\nKEY=value\n")
	require.Equal(t, "", out["EMPTY"])
	require.Equal(t, "value", out["KEY"])
}

func TestParseSkipsMalformedLines(t *testing.T) {
	out := parse("NO_EQUAL_SIGN\nK=v\n=no_key\n")
	require.Len(t, out, 1)
	require.Equal(t, "v", out["K"])
}

func writeEnv(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// TestLoadSurfacesNonENOENTReadError hits the branch where os.ReadFile
// fails for a reason other than "file doesn't exist" — we simulate by
// pointing Load at a path that exists as a directory. ReadFile on a
// directory returns EISDIR on Linux / macOS.
func TestLoadSurfacesNonENOENTReadError(t *testing.T) {
	dir := t.TempDir() // directory, not a file
	err := Load(dir)
	require.Error(t, err, "Load must surface non-ENOENT errors")
}

// TestLoadNearestFromRootStopsCleanly exercises the walk-up termination
// path (filepath.Dir(dir) == dir at the root of the filesystem).
func TestLoadNearestFromRootStopsCleanly(t *testing.T) {
	path, err := LoadNearest("/")
	require.NoError(t, err)
	// If by some accident /.env exists on the test host, this still
	// returns that path; but it must never error.
	_ = path
}

// TestLoadNearestFromEmptyStringUsesCWD — with an empty start arg, the
// function calls os.Getwd; verify the happy path returns without error
// (CWD inside the Go test binary will always resolve).
func TestLoadNearestFromEmptyStringUsesCWD(t *testing.T) {
	path, err := LoadNearest("")
	// Test CWD may or may not have a .env above it — either a found
	// path or empty string is acceptable. We only assert no error.
	require.NoError(t, err)
	_ = path
}

// TestParseUnterminatedQuoteBreaksCleanly exercises the branch where a
// multi-line quoted value runs off the end of the file without seeing a
// closing quote. The parser keeps what it has rather than erroring.
func TestParseUnterminatedQuoteBreaksCleanly(t *testing.T) {
	out := parse("KEY=\"line1\nline2\nline3\n") // no closing quote
	require.Equal(t, "line1\nline2\nline3\n", out["KEY"])
}

// TestLoadNearestSkipsDirectoryNamedDotEnv exercises findUp's check
// that the discovered path is a file, not a directory. If a dir named
// ".env" exists mid-walk, walk continues upward.
func TestLoadNearestSkipsDirectoryNamedDotEnv(t *testing.T) {
	top := t.TempDir()
	// Real .env file at the top.
	require.NoError(t, os.WriteFile(filepath.Join(top, ".env"),
		[]byte("ISOBAR_TEST_SKIPDIR=topfile\n"), 0o600))

	// Subdirectory with a ".env" directory (not a file) in it.
	sub := filepath.Join(top, "sub")
	require.NoError(t, os.MkdirAll(filepath.Join(sub, ".env"), 0o750))

	t.Setenv("ISOBAR_TEST_SKIPDIR", "")
	found, err := LoadNearest(sub)
	require.NoError(t, err)
	// Walk must have skipped sub/.env (a dir) and found top/.env (a file).
	require.Equal(t, filepath.Join(top, ".env"), found)
	require.Equal(t, "topfile", os.Getenv("ISOBAR_TEST_SKIPDIR"))
}
