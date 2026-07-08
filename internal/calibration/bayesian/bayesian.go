// Package bayesian implements precision-weighted Gaussian posterior updates
// of an MOS prior using real-time intraday temperature observations.
//
// See ADR-0010 for motivation and math. The package implements:
//
//	1/σ²_post = 1/σ²_prior + 1/σ²_obs
//	μ_post    = σ²_post · (μ_prior/σ²_prior + μ_obs/σ²_obs)
//
// where μ_obs = R(t) + E[Δ | h_remain, station, variable] and σ²_obs is
// the empirical residual variance loaded from a kernel JSON file. R(t)
// is the running extremum (max for tmax, min for tmin) of the observed
// hourly temperature trajectory through trade time t; h_remain is the
// integer hours from t to civil-day close.
//
// The package is a leaf: pure math and a kernel loader. The fitter that
// produces the kernel JSON lives in cmd/calibrate-bayesian. Integration
// into the edge-scan path is gated on ADR-0011's held-out evaluation.
package bayesian

// KernelSchemaVersion identifies the on-disk shape of a Kernel JSON.
// Bump on any change to the entry key format, the Residual fields, or
// the math interpretation of E[Δ] / Var[Δ]. Loaders must refuse
// mismatched versions rather than silently reading old files into new
// code.
const KernelSchemaVersion = "isobar.bayesian/v1"

// MinObsVariance is the floor on σ²_obs in °F². Without a floor, an
// empirically noisy kernel bin with Var[Δ]≈0 would collapse the
// posterior to a point estimate, which is overconfident given upstream
// observation error (Open-Meteo station alignment, hourly-vs-CLI
// timing slack). 0.25°F² = 0.5°F stdev — conservative but defensible
// as the lower bound on "how well can we predict TMAX from the
// running max alone".
const MinObsVariance = 0.25

// TerminalSigma is the posterior σ used when h_remain = 0. At
// settlement time the running extremum is the answer modulo data
// latency; the residual variance reflects only the Open-Meteo
// hourly-vs-NWS-CLI source mismatch. 0.5°F is the operational floor.
const TerminalSigma = 0.5

// Prior is the upstream model's predictive Gaussian for the day's
// extremum (TMAX or TMIN). In the integration path this comes from
// mos.Predict; the bayesian package is agnostic to the source so
// long as Sigma > 0.
type Prior struct {
	Mu    float64 // °F
	Sigma float64 // °F, > 0
}

// Observation is the real-time intraday view of the day's running
// extremum and how much of the day remains. RunningExtremum is the
// max-so-far for tmax or min-so-far for tmin in °F. HoursRemaining
// is the integer hours from trade time t to civil-day close in the
// station's local timezone (matching how Open-Meteo's
// historical-forecast-api groups its hourly outputs).
type Observation struct {
	Station         string  // e.g. "KNYC"
	Variable        string  // "tmax" | "tmin"
	RunningExtremum float64 // °F
	HoursRemaining  int     // ≥ 0
}

// Posterior is the precision-weighted Gaussian update of Prior by
// Observation against the loaded Kernel. Method records which branch
// produced the result, useful for auditing the prior-only fallback
// rate in production (see ADR-0010 canary section):
//
//	"bayes-gauss" — both prior and obs contributed (the standard path)
//	"obs-only"    — h_remain = 0, posterior = observation
//	"prior-only"  — kernel missing or prior invalid, posterior = prior
//
// PriorWeight and ObsWeight are σ²_post / σ²_prior and σ²_post /
// σ²_obs respectively. They sum to 1 in the standard "bayes-gauss"
// case and degenerate (one is 1, the other is 0) in the obs-only and
// prior-only branches.
type Posterior struct {
	Mu          float64
	Sigma       float64
	PriorWeight float64
	ObsWeight   float64
	Method      string
}

// Residual is the kernel's empirical summary of Δ = actual_extremum −
// R(t) for one (station, variable, hours_remaining) bin. EDelta is the
// mean residual (positive for tmax, negative for tmin in expectation);
// VarDelta is the sample variance (always ≥ 0); N is the count of paired
// observations the fitter used.
type Residual struct {
	EDelta   float64 `json:"e_delta"`
	VarDelta float64 `json:"var_delta"`
	N        int     `json:"n"`
}

// Kernel is the on-disk representation of empirical residual moments.
// Entries are keyed as "STATION/VARIABLE" (slash separator, e.g.
// "KNYC/tmax") then by integer hours_remaining (1-hour bins). The
// outer SchemaVersion gates Load-time compatibility; FitWindow records
// the human-readable date range the kernel was fitted on for audit.
type Kernel struct {
	SchemaVersion string                      `json:"schema_version"`
	FitWindow     string                      `json:"fit_window"`
	Entries       map[string]map[int]Residual `json:"entries"`
}

// MinKernelN is the smallest number of paired observations we'll
// accept in a Residual entry. Below this the empirical (mean, var)
// estimates are too noisy to drive a posterior collapse. LoadKernel
// rejects entries below this threshold loudly rather than returning a
// degraded kernel that the math layer can't tell apart from a
// well-fitted one.
const MinKernelN = 5

// EntryKey returns the canonical "STATION/VARIABLE" outer key for a
// Kernel.Entries lookup. Exported so the fitter and integration
// callers don't have to re-derive the separator convention.
func EntryKey(station, variable string) string {
	return station + "/" + variable
}
