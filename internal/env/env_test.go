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
