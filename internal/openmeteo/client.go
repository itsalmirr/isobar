// Package openmeteo polls the Open-Meteo ensemble forecast API.
//
// The free tier exposes the 31-member NOAA GFS ensemble — the data that
// lets us price Kalshi weather brackets against a probabilistic distribution
// instead of a single point forecast. Responses are emitted as raw JSON
// through OnFetch so the caller (recorder) can persist them unchanged.
package openmeteo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Location identifies one geographic point to poll.
type Location struct {
	Name string
	Lat  float64
	Lon  float64
}

// Options configures the poller.
type Options struct {
	BaseURL      string
	Model        string   // e.g., "gfs_seamless"
	Variables    []string // hourly variables, e.g., ["temperature_2m"]
	Locations    []Location
	ForecastDays int
	PollInterval time.Duration

	Logger *slog.Logger
	Client *http.Client
	// OnFetch is invoked for each successful fetch with the raw JSON
	// response body. The byte slice is freshly allocated per call.
	OnFetch func(loc Location, raw []byte)
}

// Poller emits raw JSON ensemble responses on a fixed interval.
type Poller struct {
	opts   Options
	logger *slog.Logger
}

// NewPoller validates options and returns a Poller. Call Run to start.
func NewPoller(opts Options) (*Poller, error) {
	if opts.BaseURL == "" {
		return nil, errors.New("openmeteo: base_url required")
	}
	if opts.PollInterval < time.Minute {
		return nil, errors.New("openmeteo: poll_interval must be >= 1m")
	}
	if len(opts.Locations) == 0 {
		return nil, errors.New("openmeteo: at least one location required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.OnFetch == nil {
		opts.OnFetch = func(Location, []byte) {}
	}
	if opts.ForecastDays <= 0 {
		opts.ForecastDays = 3
	}
	if opts.Model == "" {
		opts.Model = "gfs_seamless"
	}
	return &Poller{opts: opts, logger: opts.Logger.With("component", "openmeteo")}, nil
}

// Run polls locations on a fixed interval until ctx is cancelled.
// The first fetch happens immediately so Phase 0 has data without waiting
// for the first tick.
func (p *Poller) Run(ctx context.Context) error {
	t := time.NewTicker(p.opts.PollInterval)
	defer t.Stop()

	p.fetchAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			p.fetchAll(ctx)
		}
	}
}

func (p *Poller) fetchAll(ctx context.Context) {
	for _, loc := range p.opts.Locations {
		if ctx.Err() != nil {
			return
		}
		raw, err := p.fetchOne(ctx, loc)
		if err != nil {
			p.logger.Warn("fetch failed", "loc", loc.Name, "err", err)
			continue
		}
		p.opts.OnFetch(loc, raw)
	}
}

func (p *Poller) fetchOne(ctx context.Context, loc Location) ([]byte, error) {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(loc.Lat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(loc.Lon, 'f', 4, 64))
	q.Set("models", p.opts.Model)
	q.Set("forecast_days", strconv.Itoa(p.opts.ForecastDays))
	q.Set("hourly", strings.Join(p.opts.Variables, ","))
	q.Set("timezone", "UTC")

	endpoint := p.opts.BaseURL + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := p.opts.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 256))
	}
	return body, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
