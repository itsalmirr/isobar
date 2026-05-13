package bayesian

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
)

// Update returns the precision-weighted Gaussian posterior of prior
// after folding in obs against kernel k. See ADR-0010 for the math
// and edge-case rationale.
//
// Branch summary:
//
//   - prior.Sigma ≤ 0          → prior-only (defensive — invalid prior)
//   - obs.HoursRemaining == 0  → obs-only   (settlement reached; R(t) is answer)
//   - kernel nil or entry miss → prior-only (no empirical basis to update)
//   - else                     → bayes-gauss (the standard path)
func Update(prior Prior, obs Observation, k *Kernel) Posterior {
	if prior.Sigma <= 0 {
		return Posterior{
			Mu:          prior.Mu,
			Sigma:       prior.Sigma,
			PriorWeight: 1,
			ObsWeight:   0,
			Method:      "prior-only",
		}
	}
	if obs.HoursRemaining == 0 {
		return Posterior{
			Mu:          obs.RunningExtremum,
			Sigma:       TerminalSigma,
			PriorWeight: 0,
			ObsWeight:   1,
			Method:      "obs-only",
		}
	}
	if k == nil {
		return priorOnly(prior)
	}
	bin, ok := k.Entries[EntryKey(obs.Station, obs.Variable)]
	if !ok {
		return priorOnly(prior)
	}
	r, ok := bin[obs.HoursRemaining]
	if !ok {
		return priorOnly(prior)
	}

	// Clamp σ²_obs to the floor. The kernel may legitimately report
	// Var[Δ] below MinObsVariance for very late hours; the floor
	// reflects upstream observation error that the kernel fitter
	// cannot see (Open-Meteo hourly vs NWS CLI source mismatch).
	varObs := r.VarDelta
	if varObs < MinObsVariance {
		varObs = MinObsVariance
	}
	varPrior := prior.Sigma * prior.Sigma

	// μ_obs = R(t) + E[Δ]. The fitter is responsible for the sign of
	// E[Δ] (positive for tmax, negative for tmin); this function just
	// trusts the kernel's convention.
	muObs := obs.RunningExtremum + r.EDelta

	// Precision-weighted update. Carry σ²_post explicitly so PriorWeight
	// and ObsWeight read in the same units the math is written in.
	precPost := 1/varPrior + 1/varObs
	varPost := 1 / precPost
	muPost := varPost * (prior.Mu/varPrior + muObs/varObs)

	return Posterior{
		Mu:          muPost,
		Sigma:       math.Sqrt(varPost),
		PriorWeight: varPost / varPrior,
		ObsWeight:   varPost / varObs,
		Method:      "bayes-gauss",
	}
}

// priorOnly is the fail-safe return when the kernel cannot inform the
// posterior. We never tighten σ without empirical justification.
func priorOnly(prior Prior) Posterior {
	return Posterior{
		Mu:          prior.Mu,
		Sigma:       prior.Sigma,
		PriorWeight: 1,
		ObsWeight:   0,
		Method:      "prior-only",
	}
}

// LoadKernel reads + validates a kernel JSON. Enforced invariants:
//
//   - schema_version exactly matches KernelSchemaVersion
//   - entries map is non-nil (an empty map is allowed; callers will
//     then see prior-only for every Update)
//   - each Residual has N ≥ MinKernelN and VarDelta ≥ 0
//
// Errors are wrapped with the offending bin key when applicable so
// the operator can fix the kernel without grepping for "ambiguous"
// failures.
func LoadKernel(path string) (*Kernel, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is operator-supplied calibration file
	if err != nil {
		return nil, fmt.Errorf("bayesian: read kernel %s: %w", path, err)
	}
	var k Kernel
	if err := json.Unmarshal(raw, &k); err != nil {
		return nil, fmt.Errorf("bayesian: parse kernel %s: %w", path, err)
	}
	if k.SchemaVersion != KernelSchemaVersion {
		return nil, fmt.Errorf("bayesian: kernel %s: schema_version %q, want %q",
			path, k.SchemaVersion, KernelSchemaVersion)
	}
	if k.Entries == nil {
		return nil, fmt.Errorf("bayesian: kernel %s: entries map is null", path)
	}
	for outer, bin := range k.Entries {
		for h, r := range bin {
			if r.N < MinKernelN {
				return nil, fmt.Errorf("bayesian: kernel %s: bin %s/h=%d has n=%d, need ≥ %d",
					path, outer, h, r.N, MinKernelN)
			}
			if r.VarDelta < 0 {
				return nil, fmt.Errorf("bayesian: kernel %s: bin %s/h=%d has var_delta=%v < 0",
					path, outer, h, r.VarDelta)
			}
		}
	}
	return &k, nil
}
