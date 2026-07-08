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
		"ISOBAR_KALSHI_PRIVATE_KEY", "KALSHI_PRIVATE_KEY", "KALSHI_API_KEY",
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
    - { name: NY, lat: 40.7794, lon: -73.9692 }
`), 0o600))

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
	require.NoError(t, os.WriteFile(path, []byte("data_dir: [not a string}\n"), 0o600))

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
	c.Kalshi.Channels = []string{"orderbook_delta"}
	// No APIKeyID, no PEM source.
	err := c.Validate()
	require.ErrorContains(t, err, "KALSHI_API_KEY_ID")

	c.Kalshi.APIKeyID = "k"
	err = c.Validate()
	require.ErrorContains(t, err, "KALSHI_PRIVATE_KEY")
}

func TestValidateKalshiRequiresSeriesAndChannels(t *testing.T) {
	c := Defaults()
	c.Kalshi.Enabled = true
	c.Kalshi.APIKeyID = "k"
	c.Kalshi.PrivateKey = "pem"
	c.Kalshi.Channels = []string{"orderbook_delta"}
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
	c.OpenMeteo.PollInterval = 5 * time.Minute
	// An empty variable list must be rejected: it produces an empty `&hourly=`
	// query that Open-Meteo answers with a permanent 400, so a green boot would
	// record nothing forever.
	require.ErrorContains(t, c.Validate(), "variables")

	c.OpenMeteo.Variables = []string{"temperature_2m"}
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

func TestOverlayEnvPrivateKeyPathAndInline(t *testing.T) {
	clearAllKnownEnv(t)
	t.Setenv("KALSHI_PRIVATE_KEY_PATH", "/path/to/key.pem")
	t.Setenv("KALSHI_API_KEY", "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----")

	cfg := Defaults()
	overlayEnv(&cfg)
	require.Equal(t, "/path/to/key.pem", cfg.Kalshi.PrivateKeyPath)
	require.Contains(t, cfg.Kalshi.PrivateKey, "BEGIN RSA PRIVATE KEY")
}

// KALSHI_PRIVATE_KEY is the current primary name; it must win over the
// legacy KALSHI_API_KEY alias when both are set.
func TestOverlayEnvPrivateKeyNameWinsOverLegacy(t *testing.T) {
	clearAllKnownEnv(t)
	t.Setenv("KALSHI_PRIVATE_KEY", "-----BEGIN RSA PRIVATE KEY-----\nnew\n-----END RSA PRIVATE KEY-----")
	t.Setenv("KALSHI_API_KEY", "-----BEGIN RSA PRIVATE KEY-----\nold\n-----END RSA PRIVATE KEY-----")

	cfg := Defaults()
	overlayEnv(&cfg)
	require.Contains(t, cfg.Kalshi.PrivateKey, "new")
}

func TestValidateKalshiRequiresWSURL(t *testing.T) {
	c := Defaults()
	c.Kalshi.Enabled = true
	c.Kalshi.WSURL = "" // clear the default
	c.Kalshi.APIKeyID = "k"
	c.Kalshi.PrivateKey = "pem"
	c.Kalshi.Series = []string{"KXHIGHNY"}
	c.Kalshi.Channels = []string{"orderbook_delta"}
	require.ErrorContains(t, c.Validate(), "ws_url")
}

func TestValidateOpenMeteoRequiresBaseURL(t *testing.T) {
	c := Defaults()
	c.OpenMeteo.Enabled = true
	c.OpenMeteo.BaseURL = "" // clear the default
	c.OpenMeteo.Locations = []OpenMeteoLocation{{Name: "NY", Lat: 40, Lon: -73}}
	c.OpenMeteo.PollInterval = 5 * time.Minute
	require.ErrorContains(t, c.Validate(), "base_url")
}

// TestLoadReturnsValidationError verifies Load surfaces Validate errors,
// not just read/parse errors (a YAML-valid but semantically-invalid file
// must still fail Load).
func TestLoadReturnsValidationError(t *testing.T) {
	clearAllKnownEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "unqueued.yaml")
	// Parseable YAML that violates the "queue_size > 0" invariant.
	require.NoError(t, os.WriteFile(path, []byte(`
journal:
  queue_size: 0
  max_file_bytes: 1024
`), 0o600))
	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "queue_size")
}

// TestValidateStationCoordsAcceptsCanonical exercises the happy path:
// each known location with its NWS-canonical Lat/Lon must pass.
func TestValidateStationCoordsAcceptsCanonical(t *testing.T) {
	locs := []OpenMeteoLocation{
		{Name: "NY", Lat: 40.7794, Lon: -73.9692},
		{Name: "MIA", Lat: 25.7959, Lon: -80.2870},
		{Name: "LAX", Lat: 33.9425, Lon: -118.4081},
		{Name: "CHI", Lat: 41.7842, Lon: -87.7553},
	}
	require.NoError(t, validateStationCoords(locs))
}

// TestValidateStationCoordsRejectsORDForCHI is the regression guard
// for the specific bug audit/chi-blast-radius.md describes: ORD coords
// (41.9742, -87.9073) in the CHI slot. Validator must reject and the
// error must name the station, both coord pairs, and the delta.
func TestValidateStationCoordsRejectsORDForCHI(t *testing.T) {
	err := validateStationCoords([]OpenMeteoLocation{
		{Name: "CHI", Lat: 41.9742, Lon: -87.9073},
	})
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, "CHI")
	require.Contains(t, msg, "KMDW")
	require.Contains(t, msg, "41.9742")
	require.Contains(t, msg, "41.7842")
}

// TestValidateStationCoordsToleratesSubMilliDegreeDrift verifies the
// tolerance is real: a 0.0005° offset (under the 0.001° threshold)
// passes. This protects the example file from being too restrictive
// against operator-rounded values.
func TestValidateStationCoordsToleratesSubMilliDegreeDrift(t *testing.T) {
	require.NoError(t, validateStationCoords([]OpenMeteoLocation{
		{Name: "CHI", Lat: 41.7847, Lon: -87.7548}, // ±0.0005 from canonical
	}))
}

// TestValidateStationCoordsSkipsUnknownLocation documents the
// fall-through behaviour: a Name not in the canonical registry is
// silently accepted (so adding a new series doesn't require a market
// package change before the operator can poll it).
func TestValidateStationCoordsSkipsUnknownLocation(t *testing.T) {
	require.NoError(t, validateStationCoords([]OpenMeteoLocation{
		{Name: "ATL", Lat: 33.6367, Lon: -84.4281}, // Atlanta — not in registry
	}))
}

// TestLoadExampleConfig is the shipped-artifact guard: the Quickstart
// tells operators to `cp config/config.example.yaml config/config.yaml`,
// so the example itself must pass Load end-to-end — including the
// canonical station-coordinate validation. (Regression: the example
// shipped downtown-Miami coords for MIA, ~3.5 km from KMIA, so a fresh
// checkout following the README refused to boot with a coord-drift
// error.)
func TestLoadExampleConfig(t *testing.T) {
	clearAllKnownEnv(t)
	t.Setenv("KALSHI_API_KEY_ID", "k")
	t.Setenv("KALSHI_PRIVATE_KEY_PATH", "/tmp/fake.pem")

	cfg, err := Load(filepath.Join("..", "..", "config", "config.example.yaml"))
	require.NoError(t, err, "the shipped example config must boot")
	require.True(t, cfg.Kalshi.Enabled)
	require.True(t, cfg.OpenMeteo.Enabled)
	require.Len(t, cfg.OpenMeteo.Locations, 4)
}

// TestLoadFailsOnDriftedCHICoords is the end-to-end Load-level guard:
// a YAML file with the historical ORD coords for CHI must cause Load
// to return an error rather than silently boot the recorder.
func TestLoadFailsOnDriftedCHICoords(t *testing.T) {
	clearAllKnownEnv(t)
	t.Setenv("KALSHI_API_KEY_ID", "k")
	t.Setenv("KALSHI_PRIVATE_KEY_PATH", "/tmp/fake.pem")

	dir := t.TempDir()
	path := filepath.Join(dir, "chi-ord.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
kalshi:
  enabled: true
  ws_url: wss://example/ws
  series: [KXHIGHCHI]
  channels: [orderbook_delta]

openmeteo:
  enabled: true
  base_url: https://weather.example
  poll_interval: 5m
  variables: [temperature_2m]
  locations:
    - { name: CHI, lat: 41.9742, lon: -87.9073 }
`), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "CHI")
	require.Contains(t, err.Error(), "KMDW")
}
