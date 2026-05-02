// Package mos implements Model Output Statistics calibration for daily
// temperature forecasts. Same shape as NGR (Gneiting et al. 2005) but uses
// per-bin residual variance rather than ensemble spread for σ.
//
// One-line summary: replace raw forecast f with calibrated gaussian
// (μ = a + b·f, σ = stdev_of_residuals_in_bin) and price brackets via
// the standard-normal CDF.
//
// Theory: Glahn & Lowry (1972), "The Use of Model Output Statistics in
// Objective Weather Forecasting". The shipping decision to use MOS over
// full NGR for first-pass calibration is documented in ADR-0008.
package mos

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// Pair is one training/eval datum: a forecast for (station, valid_date,
// variable) and the observed truth for the same key.
type Pair struct {
	Station   string    // "KNYC", "KMIA", "KLAX", "KMDW"
	ValidDate time.Time // station-local civil date, midnight UTC
	Variable  string    // "tmax" | "tmin"
	Forecast  float64   // °F
	Observed  float64   // °F
}

// BinCoeffs are the fitted parameters for one (station, season, variable) bin.
type BinCoeffs struct {
	A        float64 `json:"a"`         // intercept (°F)
	B        float64 `json:"b"`         // slope (dimensionless)
	Sigma    float64 `json:"sigma"`     // residual stdev (°F)
	NTrain   int     `json:"n_train"`   // training pair count
	NHoldout int     `json:"n_holdout"` // holdout pair count (recorded but not used in fit)
}

// Coefficients is the full fitted model: one BinCoeffs per
// (station, season, variable) bin keyed as "STA|SEASON|VAR".
type Coefficients struct {
	FittedAt           time.Time            `json:"fitted_at"`
	TrainingWindowEnd  string               `json:"training_window_end"`  // YYYY-MM-DD inclusive
	HoldoutWindowStart string               `json:"holdout_window_start"` // YYYY-MM-DD inclusive
	Bins               map[string]BinCoeffs `json:"bins"`
}

// MinTrainPerBin is the smallest number of training pairs we'll fit a
// linear model on. Below this we drop the bin and Predict returns ok=false
// rather than producing a noisy estimate that downstream code will trust.
const MinTrainPerBin = 10

// Fit produces Coefficients from a list of paired observations. Pairs are
// split into train/holdout by valid_date: pairs with valid_date in the
// final `holdoutDays` of the input window are holdout; the rest are train.
//
// Bins with fewer than MinTrainPerBin training pairs are dropped from the
// output (Predict returns ok=false for missing bins).
func Fit(pairs []Pair, holdoutDays int) (Coefficients, error) {
	if len(pairs) == 0 {
		return Coefficients{}, errors.New("mos: no pairs to fit")
	}
	if holdoutDays < 0 {
		return Coefficients{}, fmt.Errorf("mos: negative holdoutDays %d", holdoutDays)
	}

	maxDate := pairs[0].ValidDate
	for _, p := range pairs[1:] {
		if p.ValidDate.After(maxDate) {
			maxDate = p.ValidDate
		}
	}
	holdoutStart := maxDate.Add(-time.Duration(holdoutDays) * 24 * time.Hour)
	trainEnd := holdoutStart.Add(-24 * time.Hour)

	// Group pairs by bin into train/holdout.
	type bucket struct {
		train, holdout []Pair
	}
	buckets := make(map[string]*bucket)
	for _, p := range pairs {
		key := binKey(p.Station, season(p.ValidDate), p.Variable)
		b, ok := buckets[key]
		if !ok {
			b = &bucket{}
			buckets[key] = b
		}
		if !p.ValidDate.Before(holdoutStart) {
			b.holdout = append(b.holdout, p)
		} else {
			b.train = append(b.train, p)
		}
	}

	out := Coefficients{
		FittedAt:           time.Now().UTC(),
		TrainingWindowEnd:  trainEnd.Format("2006-01-02"),
		HoldoutWindowStart: holdoutStart.Format("2006-01-02"),
		Bins:               make(map[string]BinCoeffs, len(buckets)),
	}

	for key, b := range buckets {
		if len(b.train) < MinTrainPerBin {
			continue
		}
		c, ok := fitOLS(b.train)
		if !ok {
			continue
		}
		c.NTrain = len(b.train)
		c.NHoldout = len(b.holdout)
		out.Bins[key] = c
	}

	if len(out.Bins) == 0 {
		return Coefficients{}, fmt.Errorf("mos: no bin had at least %d training pairs", MinTrainPerBin)
	}
	return out, nil
}

// fitOLS performs closed-form ordinary least squares on the (forecast,
// observed) pairs and returns slope, intercept, and residual stdev.
// Returns ok=false when the design matrix is degenerate (e.g., constant
// forecast values yield zero variance).
func fitOLS(pairs []Pair) (BinCoeffs, bool) {
	n := len(pairs)
	if n < 2 {
		return BinCoeffs{}, false
	}

	var sumX, sumY, sumXX, sumXY float64
	for _, p := range pairs {
		sumX += p.Forecast
		sumY += p.Observed
		sumXX += p.Forecast * p.Forecast
		sumXY += p.Forecast * p.Observed
	}
	nf := float64(n)
	meanX := sumX / nf
	meanY := sumY / nf
	denom := sumXX - nf*meanX*meanX
	if denom <= 0 {
		return BinCoeffs{}, false
	}
	b := (sumXY - nf*meanX*meanY) / denom
	a := meanY - b*meanX

	// Residual stdev (sample, n-1 denominator).
	var ss float64
	for _, p := range pairs {
		r := p.Observed - (a + b*p.Forecast)
		ss += r * r
	}
	sigma := math.Sqrt(ss / float64(n-1))
	if sigma == 0 || math.IsNaN(sigma) || math.IsInf(sigma, 0) {
		return BinCoeffs{}, false
	}
	return BinCoeffs{A: a, B: b, Sigma: sigma}, true
}

// Predict returns the calibrated (μ, σ) for a forecast at a given station
// and date, plus ok=true if a fitted bin exists. Variable must be "tmax"
// or "tmin". Returns (0, 0, false) when the bin wasn't fit (insufficient
// training data, unknown station, etc.).
func (c Coefficients) Predict(station string, validDate time.Time, variable string, forecast float64) (mu, sigma float64, ok bool) {
	key := binKey(station, season(validDate), variable)
	bc, ok := c.Bins[key]
	if !ok {
		return 0, 0, false
	}
	return bc.A + bc.B*forecast, bc.Sigma, true
}

// ProbBelow returns Φ((threshold − μ) / σ), the probability under a
// normal(μ, σ²) that the realised value is at or below threshold. Used
// for "below" brackets.
func ProbBelow(threshold, mu, sigma float64) float64 {
	if sigma <= 0 {
		// Degenerate: σ=0 means deterministic prediction. Fall back to
		// indicator: P=1 if μ<=threshold, else 0.
		if mu <= threshold {
			return 1
		}
		return 0
	}
	z := (threshold - mu) / sigma
	return 0.5 * (1 + math.Erf(z/math.Sqrt2))
}

// ProbAbove is 1 − ProbBelow.
func ProbAbove(threshold, mu, sigma float64) float64 {
	return 1 - ProbBelow(threshold, mu, sigma)
}

// season returns "DJF", "MAM", "JJA", "SON" for the meteorological season
// containing the date's month. Northern Hemisphere convention (we trade
// US weather brackets only).
func season(t time.Time) string {
	switch t.Month() {
	case time.December, time.January, time.February:
		return "DJF"
	case time.March, time.April, time.May:
		return "MAM"
	case time.June, time.July, time.August:
		return "JJA"
	default:
		return "SON"
	}
}

// binKey is the canonical map-key form: "STATION|SEASON|VARIABLE".
func binKey(station, season, variable string) string {
	return station + "|" + season + "|" + variable
}

// SortedBinKeys returns the bin keys in deterministic order. Useful for
// stable summary output and diffable JSON.
func (c Coefficients) SortedBinKeys() []string {
	keys := make([]string, 0, len(c.Bins))
	for k := range c.Bins {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
