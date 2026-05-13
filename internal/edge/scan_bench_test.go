package edge

import (
	"fmt"
	"testing"
)

// buildMultiModelPoll constructs an in-memory *Poll with gfsN GFS members
// and ecmwfN ECMWF members, mimicking what ParsePoll emits for a real
// multi-model Open-Meteo response. The temperature series themselves are
// not load-bearing for pollModelSet — only the member-ID prefix patterns
// are exercised — so values are deliberately empty.
//
// Poll.ModelSet is populated here too (mirroring what ParsePoll does),
// so callers can exercise the cached read path.
func buildMultiModelPoll(gfsN, ecmwfN int) *Poll {
	members := make(map[string][]float64, gfsN+ecmwfN)
	// GFS: "ncep_gefs_seamless:00" .. "ncep_gefs_seamless:NN"
	for i := 0; i < gfsN; i++ {
		members[fmt.Sprintf("ncep_gefs_seamless:%02d", i)] = nil
	}
	// ECMWF: "ecmwf_ifs025_ensemble:00" .. "ecmwf_ifs025_ensemble:NN"
	for i := 0; i < ecmwfN; i++ {
		members[fmt.Sprintf("ecmwf_ifs025_ensemble:%02d", i)] = nil
	}
	return &Poll{
		Members:  members,
		ModelSet: pollModelSetFromMembers(members),
	}
}

// buildExtremesMap constructs the map[string]MemberDaily that the scan
// hot path passes to pollModelSet, keyed by the same member IDs as
// poll.Members. Values are zero-valued (the scan loop only consumes
// extremes for TMAX/TMIN reads, which we do not exercise here).
func buildExtremesMap(poll *Poll) map[string]MemberDaily {
	out := make(map[string]MemberDaily, len(poll.Members))
	for id := range poll.Members {
		out[id] = MemberDaily{}
	}
	return out
}

// BenchmarkPollModelSet_NoCache captures the per-trade allocation of
// pollModelSet on a representative multi-model poll (31 GFS + 31 ECMWF
// members, matching what edge-scan sees today). Numbers from this
// benchmark are the baseline against which any caching change is
// measured. The audit (audit/01b-prior-review.md §F2.2) flagged this
// path as allocating per trade despite the model set being invariant
// per *Poll.
func BenchmarkPollModelSet_NoCache(b *testing.B) {
	poll := buildMultiModelPoll(31, 31)
	extremes := buildExtremesMap(poll)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = pollModelSet(extremes)
	}
}

// BenchmarkPollModelSet_Cached measures the cached read path that the
// scan hot path now uses: a single field read on *Poll. Mirrors the
// structure of BenchmarkPollModelSet_NoCache exactly (same poll, same
// loop body) so deltas are directly comparable. Allocs/op should be 0;
// ns/op should be small constant (load+return).
func BenchmarkPollModelSet_Cached(b *testing.B) {
	poll := buildMultiModelPoll(31, 31)
	_ = buildExtremesMap(poll) // parity with NoCache setup; not read in the loop

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = poll.ModelSet
	}
}
