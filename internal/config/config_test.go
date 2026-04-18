package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// clearAllKnownEnv removes every variable overlay the package knows about so
// tests can start from a clean slate regardless of the ambient environment.
// Uses t.Setenv so Go restores originals on cleanup.
func clearAllKnownEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"ISOBAR_KALSHI_API_KEY_ID", "KALSHI_API_KEY_ID",
		"ISOBAR_KALSHI_PRIVATE_KEY_PATH", "KALSHI_PRIVATE_KEY_PATH",
		"ISOBAR_KALSHI_PRIVATE_KEY", "KALSHI_API_KEY",
		"ISOBAR_DATA_DIR",
	} {
		t.Setenv(k, "")
	}
}

func TestDefaultsAreUsable(t *testing.T) {
	d := Defaults()
	require.Equal(t, "./data", d.DataDir)
	require.Positive(t, d.Journal.QueueSize)
	require.Positive(t, d.Journal.MaxFileBytes)
	require.Equal(t, 500*time.Millisecond, d.Kalshi.Reconnect.InitialBackoff)
	require.Equal(t, 30*time.Second, d.Kalshi.Reconnect.MaxBackoff)
	require.Equal(t, "gfs_seamless", d.OpenMeteo.Model)
}

func TestLoadNoPathWithAllDisabledUsesDefaults(t *testing.T) {
	clearAllKnownEnv(t)
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, "./data", cfg.DataDir)
	require.False(t, cfg.Kalshi.Enabled)
	require.False(t, cfg.OpenMeteo.Enabled)
}

func TestLoadFromYAMLOverlay(t *testing.T) {
	clearAllKnownEnv(t)
	t.Setenv("KALSHI_API_KEY_ID", "abc-12345678")
	t.Setenv("KALSHI_PRIVATE_KEY_PATH", "/tmp/fake.pem")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
data_dir: ./custom-data
journal:
  max_file_bytes: 524288
  max_file_age: 30m
  flush_every: 100ms
  queue_size: 1024
  compress_on_rotate: true

kalshi:
  enabled: true
  api_base: https://example/api
  ws_url: wss://example/ws
  series: [KXHIGHNY]
  channels: [ticker_v2]

openmeteo:
  enabled: true
  base_url: https://weather.example
  poll_interval: 5m
  variables: [temperature_2m]
  locations:
    - { name: NY, series: [KXHIGHNY], lat: 40.0, lon: -73.0 }
`), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)

	require.Equal(t, "./custom-data", cfg.DataDir)
	require.Equal(t, int64(524288), cfg.Journal.MaxFileBytes)
	require.Equal(t, 30*time.Minute, cfg.Journal.MaxFileAge)
	require.True(t, cfg.Kalshi.Enabled)
	require.Equal(t, []string{"KXHIGHNY"}, cfg.Kalshi.Series)
	require.Equal(t, "abc-12345678", cfg.Kalshi.APIKeyID)
	require.Equal(t, "/tmp/fake.pem", cfg.Kalshi.PrivateKeyPath)
	require.True(t, cfg.OpenMeteo.Enabled)
	require.Equal(t, 5*time.Minute, cfg.OpenMeteo.PollInterval)
}

func TestLoadMissingFile(t *testing.T) {
	clearAllKnownEnv(t)
	_, err := Load(filepath.Join(t.TempDir(), "no-such-file.yaml"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "read")
}

func TestLoadInvalidYAML(t *testing.T) {
	clearAllKnownEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(path, []byte("data_dir: [not a string}\n"), 0o644))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse")
}

func TestOverlayEnvPrefersISOBARPrefix(t *testing.T) {
	clearAllKnownEnv(t)
	// Both set — prefixed must win.
	t.Setenv("KALSHI_API_KEY_ID", "bare-id")
	t.Setenv("ISOBAR_KALSHI_API_KEY_ID", "prefixed-id")

	cfg := Defaults()
	overlayEnv(&cfg)
	require.Equal(t, "prefixed-id", cfg.Kalshi.APIKeyID)
}

func TestOverlayEnvFallsBackToBare(t *testing.T) {
	clearAllKnownEnv(t)
	t.Setenv("KALSHI_API_KEY_ID", "bare-id")

	cfg := Defaults()
	overlayEnv(&cfg)
	require.Equal(t, "bare-id", cfg.Kalshi.APIKeyID)
}

func TestOverlayEnvDataDir(t *testing.T) {
	clearAllKnownEnv(t)
	t.Setenv("ISOBAR_DATA_DIR", "/var/isobar-data")

	cfg := Defaults()
	overlayEnv(&cfg)
	require.Equal(t, "/var/isobar-data", cfg.DataDir)
}

func TestValidateRejectsEmptyDataDir(t *testing.T) {
	c := Defaults()
	c.DataDir = ""
	require.ErrorContains(t, c.Validate(), "data_dir")
}

func TestValidateRejectsZeroQueueSize(t *testing.T) {
	c := Defaults()
	c.Journal.QueueSize = 0
	require.ErrorContains(t, c.Validate(), "queue_size")
}

func TestValidateRejectsNoRotation(t *testing.T) {
	c := Defaults()
	c.Journal.MaxFileBytes = 0
	c.Journal.MaxFileAge = 0
	require.ErrorContains(t, c.Validate(), "max_file")
}

func TestValidateKalshiRequiresAuth(t *testing.T) {
	c := Defaults()
	c.Kalshi.Enabled = true
	c.Kalshi.Series = []string{"KXHIGHNY"}
	c.Kalshi.Channels = []string{"ticker_v2"}
	// No APIKeyID, no PEM source.
	err := c.Validate()
	require.ErrorContains(t, err, "KALSHI_API_KEY_ID")

	c.Kalshi.APIKeyID = "k"
	err = c.Validate()
	require.ErrorContains(t, err, "KALSHI_API_KEY")
}

func TestValidateKalshiRequiresSeriesAndChannels(t *testing.T) {
	c := Defaults()
	c.Kalshi.Enabled = true
	c.Kalshi.APIKeyID = "k"
	c.Kalshi.PrivateKey = "pem"
	c.Kalshi.Channels = []string{"ticker_v2"}
	// Empty series.
	require.ErrorContains(t, c.Validate(), "series")

	c.Kalshi.Series = []string{"KXHIGHNY"}
	c.Kalshi.Channels = nil
	require.ErrorContains(t, c.Validate(), "channels")
}

func TestValidateKalshiAcceptsFullConfig(t *testing.T) {
	c := Defaults()
	c.Kalshi.Enabled = true
	c.Kalshi.APIKeyID = "k"
	c.Kalshi.PrivateKey = "pem"
	c.Kalshi.Series = []string{"KXHIGHNY"}
	c.Kalshi.Channels = []string{"ticker_v2"}
	require.NoError(t, c.Validate())
}

func TestValidateOpenMeteo(t *testing.T) {
	c := Defaults()
	c.OpenMeteo.Enabled = true
	c.OpenMeteo.BaseURL = "https://x"
	// No locations.
	require.ErrorContains(t, c.Validate(), "locations")

	c.OpenMeteo.Locations = []OpenMeteoLocation{{Name: "NY", Lat: 40, Lon: -73}}
	c.OpenMeteo.PollInterval = 30 * time.Second
	require.ErrorContains(t, c.Validate(), "poll_interval")

	c.OpenMeteo.PollInterval = 5 * time.Minute
	require.NoError(t, c.Validate())
}

func TestFirstNonEmpty(t *testing.T) {
	t.Setenv("FNE_A", "")
	t.Setenv("FNE_B", "b")
	t.Setenv("FNE_C", "c")
	require.Equal(t, "b", firstNonEmpty("FNE_A", "FNE_B", "FNE_C"))
	require.Equal(t, "", firstNonEmpty("FNE_NOT_SET_1", "FNE_NOT_SET_2"))
}
