// Package config loads the recorder's runtime configuration from a YAML
// file, overlays select fields from environment variables (ISOBAR_* prefix),
// and validates the result. The config is read once at startup and treated
// as immutable.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level runtime configuration.
type Config struct {
	DataDir   string    `yaml:"data_dir"`
	Journal   Journal   `yaml:"journal"`
	Kalshi    Kalshi    `yaml:"kalshi"`
	OpenMeteo OpenMeteo `yaml:"openmeteo"`
}

// Journal tunes the per-source append-only writers.
type Journal struct {
	MaxFileBytes     int64         `yaml:"max_file_bytes"`
	MaxFileAge       time.Duration `yaml:"max_file_age"`
	FlushEvery       time.Duration `yaml:"flush_every"`
	QueueSize        int           `yaml:"queue_size"`
	CompressOnRotate bool          `yaml:"compress_on_rotate"`
}

// Reconnect configures exponential backoff for WS connections.
type Reconnect struct {
	InitialBackoff time.Duration `yaml:"initial_backoff"`
	MaxBackoff     time.Duration `yaml:"max_backoff"`
	MaxAttempts    int           `yaml:"max_attempts"` // 0 = infinite
}

// Kalshi configures the Kalshi market-data subscriber.
type Kalshi struct {
	Enabled   bool      `yaml:"enabled"`
	APIBase   string    `yaml:"api_base"`
	WSURL     string    `yaml:"ws_url"`
	Series    []string  `yaml:"series"`
	Channels  []string  `yaml:"channels"`
	Reconnect Reconnect `yaml:"reconnect"`

	// Credentials are filled from env vars; not in YAML.
	// Provide PrivateKey (inline PEM) OR PrivateKeyPath (file). At least one
	// must be set when Enabled.
	APIKeyID       string `yaml:"-"`
	PrivateKey     string `yaml:"-"`
	PrivateKeyPath string `yaml:"-"`
}

// OpenMeteoLocation pairs a Kalshi series identifier set with NOAA grid coords.
type OpenMeteoLocation struct {
	Name   string   `yaml:"name"`
	Series []string `yaml:"series"`
	Lat    float64  `yaml:"lat"`
	Lon    float64  `yaml:"lon"`
}

// OpenMeteo configures the NOAA GFS ensemble REST poller.
type OpenMeteo struct {
	Enabled      bool                `yaml:"enabled"`
	BaseURL      string              `yaml:"base_url"`
	Model        string              `yaml:"model"`
	PollInterval time.Duration       `yaml:"poll_interval"`
	Locations    []OpenMeteoLocation `yaml:"locations"`
	Variables    []string            `yaml:"variables"`
	ForecastDays int                 `yaml:"forecast_days"`
}

// Defaults returns a Config with sensible production defaults.
// File loaders should overlay onto this so unspecified keys keep their
// defaults rather than zero values.
func Defaults() Config {
	return Config{
		DataDir: "./data",
		Journal: Journal{
			MaxFileBytes:     256 << 20, // 256 MiB
			MaxFileAge:       1 * time.Hour,
			FlushEvery:       250 * time.Millisecond,
			QueueSize:        16384,
			CompressOnRotate: true,
		},
		Kalshi: Kalshi{
			APIBase: "https://api.elections.kalshi.com/trade-api/v2",
			WSURL:   "wss://api.elections.kalshi.com/trade-api/ws/v2",
			Reconnect: Reconnect{
				InitialBackoff: 500 * time.Millisecond,
				MaxBackoff:     30 * time.Second,
			},
		},
		OpenMeteo: OpenMeteo{
			BaseURL:      "https://ensemble-api.open-meteo.com/v1/ensemble",
			Model:        "gfs_seamless",
			PollInterval: 30 * time.Minute,
			ForecastDays: 3,
		},
	}
}

// Load reads YAML from path, overlays env vars, and validates.
// path == "" loads only defaults + env (useful for ephemeral runs).
func Load(path string) (Config, error) {
	cfg := Defaults()

	if path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // path is operator-supplied config flag, not user input
		if err != nil {
			return Config{}, fmt.Errorf("config: read %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
	}

	overlayEnv(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// overlayEnv applies environment variables to cfg in place. Both bare
// (KALSHI_*) and ISOBAR_-prefixed names are accepted — bare matches the
// arbhunter convention and Kalshi's own naming, the prefix is for
// environments where multiple bots share a host.
func overlayEnv(cfg *Config) {
	if v := firstNonEmpty("ISOBAR_KALSHI_API_KEY_ID", "KALSHI_API_KEY_ID"); v != "" {
		cfg.Kalshi.APIKeyID = v
	}
	if v := firstNonEmpty("ISOBAR_KALSHI_PRIVATE_KEY_PATH", "KALSHI_PRIVATE_KEY_PATH"); v != "" {
		cfg.Kalshi.PrivateKeyPath = v
	}
	// KALSHI_PRIVATE_KEY is the current name; KALSHI_API_KEY is kept for
	// back-compat with earlier arbhunter-style configs (the name was
	// historically misleading — it's a PEM, not an opaque token).
	if v := firstNonEmpty("ISOBAR_KALSHI_PRIVATE_KEY", "KALSHI_PRIVATE_KEY", "KALSHI_API_KEY"); v != "" {
		cfg.Kalshi.PrivateKey = v
	}
	if v := os.Getenv("ISOBAR_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
}

func firstNonEmpty(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// Validate returns an error if cfg has internally-inconsistent state.
// Disabled sources are not validated past the enabled flag.
func (c Config) Validate() error {
	if strings.TrimSpace(c.DataDir) == "" {
		return errors.New("config: data_dir must be set")
	}
	if c.Journal.QueueSize <= 0 {
		return errors.New("config: journal.queue_size must be > 0")
	}
	if c.Journal.MaxFileBytes <= 0 && c.Journal.MaxFileAge <= 0 {
		return errors.New("config: at least one of journal.max_file_bytes or max_file_age must be > 0")
	}

	if c.Kalshi.Enabled {
		if c.Kalshi.WSURL == "" {
			return errors.New("config: kalshi.ws_url required when kalshi.enabled")
		}
		if c.Kalshi.APIKeyID == "" {
			return errors.New("config: KALSHI_API_KEY_ID (or ISOBAR_KALSHI_API_KEY_ID) required when kalshi.enabled")
		}
		if c.Kalshi.PrivateKey == "" && c.Kalshi.PrivateKeyPath == "" {
			return errors.New("config: set KALSHI_PRIVATE_KEY (inline PEM) or KALSHI_PRIVATE_KEY_PATH (file path) when kalshi.enabled")
		}
		if len(c.Kalshi.Series) == 0 {
			return errors.New("config: kalshi.series must list at least one series prefix")
		}
		if len(c.Kalshi.Channels) == 0 {
			return errors.New("config: kalshi.channels must list at least one channel")
		}
	}

	if c.OpenMeteo.Enabled {
		if c.OpenMeteo.BaseURL == "" {
			return errors.New("config: openmeteo.base_url required when openmeteo.enabled")
		}
		if len(c.OpenMeteo.Locations) == 0 {
			return errors.New("config: openmeteo.locations must list at least one location")
		}
		if c.OpenMeteo.PollInterval < time.Minute {
			return errors.New("config: openmeteo.poll_interval must be >= 1m to avoid rate limits")
		}
	}

	return nil
}
