package replay

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBookApplySnapshotInitializesLevels(t *testing.T) {
	b := NewBook()
	msg := json.RawMessage(`{
		"market_ticker":"KXHIGHNY-26APR22-T64",
		"market_id":"m-1",
		"yes_dollars_fp":[["0.4500","100.00"],["0.4200","50.50"]],
		"no_dollars_fp":[["0.5200","75.00"],["0.5500","12.00"]]
	}`)
	require.NoError(t, b.ApplySnapshot(msg))
	require.Equal(t, 100.0, b.Yes[45])
	require.Equal(t, 50.5, b.Yes[42])
	require.Equal(t, 75.0, b.No[52])
	require.Equal(t, 12.0, b.No[55])
	// Top-of-book
	require.Equal(t, 45, b.BestYesBid())
	require.Equal(t, 55, b.BestNoBid())
	require.Equal(t, 45, b.BestYesAsk())  // 100 - 55
	require.Equal(t, 45, b.MidYesCents()) // (45 + 45) / 2
	require.Equal(t, 0, b.SpreadYesCents())
}

func TestBookSnapshotWipesPriorState(t *testing.T) {
	b := NewBook()
	b.Yes[10] = 999
	b.No[20] = 999
	msg := json.RawMessage(`{
		"market_ticker":"X",
		"yes_dollars_fp":[["0.3000","5.00"]],
		"no_dollars_fp":[["0.7000","5.00"]]
	}`)
	require.NoError(t, b.ApplySnapshot(msg))
	require.Len(t, b.Yes, 1)
	require.Len(t, b.No, 1)
	_, hadOld := b.Yes[10]
	require.False(t, hadOld)
}

func TestBookApplyDeltaAddsAndRemoves(t *testing.T) {
	b := NewBook()
	require.NoError(t, b.ApplySnapshot(json.RawMessage(`{
		"yes_dollars_fp":[["0.4500","100.00"]],
		"no_dollars_fp":[["0.5200","75.00"]]
	}`)))

	// Add 10 at yes 45 → 110
	require.NoError(t, b.ApplyDelta(json.RawMessage(`{
		"price_dollars":"0.4500","delta_fp":"10.00","side":"yes"
	}`)))
	require.Equal(t, 110.0, b.Yes[45])

	// New level at yes 46
	require.NoError(t, b.ApplyDelta(json.RawMessage(`{
		"price_dollars":"0.4600","delta_fp":"5.00","side":"yes"
	}`)))
	require.Equal(t, 5.0, b.Yes[46])
	require.Equal(t, 46, b.BestYesBid())

	// Negative delta that zeroes a level → removal
	require.NoError(t, b.ApplyDelta(json.RawMessage(`{
		"price_dollars":"0.4600","delta_fp":"-5.00","side":"yes"
	}`)))
	_, present := b.Yes[46]
	require.False(t, present)
	require.Equal(t, 45, b.BestYesBid())

	// Negative delta that overshoots also removes (defensive)
	require.NoError(t, b.ApplyDelta(json.RawMessage(`{
		"price_dollars":"0.5200","delta_fp":"-999.00","side":"no"
	}`)))
	_, present = b.No[52]
	require.False(t, present)
	require.Equal(t, 0, b.BestNoBid())
}

func TestBookDeltaUnknownSideReturnsError(t *testing.T) {
	b := NewBook()
	err := b.ApplyDelta(json.RawMessage(`{"price_dollars":"0.50","delta_fp":"1.00","side":"maybe"}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown side")
}

func TestBookDeltaMalformedPriceOrQty(t *testing.T) {
	b := NewBook()
	require.Error(t, b.ApplyDelta(json.RawMessage(`{"price_dollars":"xx","delta_fp":"1.00","side":"yes"}`)))
	require.Error(t, b.ApplyDelta(json.RawMessage(`{"price_dollars":"0.50","delta_fp":"yy","side":"yes"}`)))
}

func TestBookTopOfBookOnEmptySide(t *testing.T) {
	b := NewBook()
	require.Equal(t, 0, b.BestYesBid())
	require.Equal(t, 0, b.BestNoBid())
	require.Equal(t, 100, b.BestYesAsk(), "empty no side → yes can go to 100")
	require.Equal(t, 0, b.MidYesCents(), "one-sided market has undefined mid; report 0")
	require.Equal(t, 100, b.SpreadYesCents())
}

func TestBookMidAndSpreadOnTwoSided(t *testing.T) {
	b := NewBook()
	// Yes bid 40, No bid 55 → Yes ask = 45, spread = 5, mid = 42
	require.NoError(t, b.ApplySnapshot(json.RawMessage(`{
		"yes_dollars_fp":[["0.4000","1"]],
		"no_dollars_fp":[["0.5500","1"]]
	}`)))
	require.Equal(t, 40, b.BestYesBid())
	require.Equal(t, 45, b.BestYesAsk())
	require.Equal(t, 42, b.MidYesCents())
	require.Equal(t, 5, b.SpreadYesCents())
}

func TestPriceDollarsToCents(t *testing.T) {
	cases := []struct {
		in   string
		want int
		err  bool
	}{
		{"0.0000", 0, false},
		{"0.0100", 1, false},
		{"0.5500", 55, false},
		{"0.9900", 99, false},
		{"1.0000", 100, false},
		{"1.5000", 0, true}, // out of range
		{"-0.10", 0, true},  // negative
		{"abc", 0, true},    // non-numeric
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := priceDollarsToCents(tc.in)
			if tc.err {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestApplyFrameToBooks(t *testing.T) {
	ResetCountersForTest()
	books := map[string]*Book{}
	walls := map[string]int64{}

	snapshotPayload := json.RawMessage(`{"market_ticker":"KXHIGHNY-26APR22-T64","yes_dollars_fp":[["0.4500","100"]],"no_dollars_fp":[["0.5200","75"]]}`)
	deltaPayload := json.RawMessage(`{"market_ticker":"KXHIGHNY-26APR22-T64","price_dollars":"0.4500","delta_fp":"10","side":"yes"}`)

	// Snapshot creates book and updates wall.
	env1 := Envelope{Wall: 100}
	f1 := KalshiFrame{Type: "orderbook_snapshot", Msg: snapshotPayload}
	malformed, err := ApplyFrameToBooks(books, walls, env1, f1)
	require.False(t, malformed)
	require.NoError(t, err)
	require.Len(t, books, 1)
	require.Equal(t, int64(100), walls["KXHIGHNY-26APR22-T64"])
	require.Equal(t, 100.0, books["KXHIGHNY-26APR22-T64"].Yes[45])
	require.True(t, books["KXHIGHNY-26APR22-T64"].IsReady())

	// Delta after snapshot updates book and wall.
	env2 := Envelope{Wall: 200}
	f2 := KalshiFrame{Type: "orderbook_delta", Msg: deltaPayload}
	malformed, err = ApplyFrameToBooks(books, walls, env2, f2)
	require.False(t, malformed)
	require.NoError(t, err)
	require.Equal(t, int64(200), walls["KXHIGHNY-26APR22-T64"])
	require.Equal(t, 110.0, books["KXHIGHNY-26APR22-T64"].Yes[45])

	// Delta before snapshot for unknown ticker is silently skipped
	// (no book exists in the map yet).
	otherDelta := json.RawMessage(`{"market_ticker":"OTHER","price_dollars":"0.5","delta_fp":"5","side":"yes"}`)
	f3 := KalshiFrame{Type: "orderbook_delta", Msg: otherDelta}
	malformed, err = ApplyFrameToBooks(books, walls, Envelope{Wall: 300}, f3)
	require.False(t, malformed)
	require.NoError(t, err)
	require.Len(t, books, 1)

	// Frame with no ticker is no-op.
	f4 := KalshiFrame{Type: "subscribed", Msg: json.RawMessage(`{}`)}
	malformed, err = ApplyFrameToBooks(books, walls, Envelope{Wall: 400}, f4)
	require.False(t, malformed)
	require.NoError(t, err)

	// Malformed snapshot returns malformed=true and a typed error.
	f5 := KalshiFrame{Type: "orderbook_snapshot", Msg: json.RawMessage(`{"market_ticker":"BAD","yes_dollars_fp":"not-an-array"}`)}
	malformed, err = ApplyFrameToBooks(books, walls, Envelope{Wall: 500}, f5)
	require.True(t, malformed)
	require.ErrorIs(t, err, ErrSnapshotApplyFailed)
	require.False(t, books["BAD"].IsReady(), "malformed snapshot must leave book uninitialized")

	// nil walls map is allowed (analyze case).
	books2 := map[string]*Book{}
	malformed, err = ApplyFrameToBooks(books2, nil, env1, f1)
	require.False(t, malformed)
	require.NoError(t, err)
	require.Len(t, books2, 1)
}

func TestBookRoundTripSnapshotThenDeltas(t *testing.T) {
	// Realistic sequence: snapshot establishes book, then a series
	// of deltas move the top of book. Verifies mid + best bid/ask
	// stay consistent throughout.
	b := NewBook()
	require.NoError(t, b.ApplySnapshot(json.RawMessage(`{
		"yes_dollars_fp":[["0.3000","10"],["0.3500","20"],["0.4000","30"]],
		"no_dollars_fp":[["0.5500","10"],["0.5800","20"],["0.6200","30"]]
	}`)))
	require.Equal(t, 40, b.BestYesBid())
	require.Equal(t, 62, b.BestNoBid())
	// Add volume at current top → same top, deeper
	require.NoError(t, b.ApplyDelta(json.RawMessage(`{"price_dollars":"0.4000","delta_fp":"5","side":"yes"}`)))
	require.Equal(t, 35.0, b.Yes[40])
	require.Equal(t, 40, b.BestYesBid())
	// Remove entire top level → best drops to 35
	require.NoError(t, b.ApplyDelta(json.RawMessage(`{"price_dollars":"0.4000","delta_fp":"-35","side":"yes"}`)))
	require.Equal(t, 35, b.BestYesBid())
}

// TestBook_IsReady_TransitionsCorrectly exercises the book lifecycle
// state machine in isolation: uninitialized → ready on successful
// snapshot, ready → uninitialized on snapshot apply error, and back
// to ready on a subsequent successful snapshot. This is the
// guarantee that ApplyFrameToBooks relies on to decide whether to
// admit deltas.
func TestBook_IsReady_TransitionsCorrectly(t *testing.T) {
	b := NewBook()

	// Fresh book: uninitialized.
	require.False(t, b.IsReady(), "new book starts uninitialized")
	require.Empty(t, b.Yes)
	require.Empty(t, b.No)

	// Successful snapshot → ready.
	require.NoError(t, b.ApplySnapshot(json.RawMessage(`{
		"yes_dollars_fp":[["0.4500","100"]],
		"no_dollars_fp":[["0.5200","75"]]
	}`)))
	require.True(t, b.IsReady(), "successful snapshot transitions to ready")
	require.Equal(t, 100.0, b.Yes[45])

	// Snapshot apply error → back to uninitialized AND maps cleared.
	require.Error(t, b.ApplySnapshot(json.RawMessage(`{"yes_dollars_fp":"not-an-array"}`)))
	require.False(t, b.IsReady(), "snapshot error reverts to uninitialized")
	require.Empty(t, b.Yes, "snapshot error must clear Yes map; stale partial state is the bug we're fixing")
	require.Empty(t, b.No, "snapshot error must clear No map; stale partial state is the bug we're fixing")

	// Recovery: a fresh successful snapshot returns to ready.
	require.NoError(t, b.ApplySnapshot(json.RawMessage(`{
		"yes_dollars_fp":[["0.4000","50"]],
		"no_dollars_fp":[["0.5500","25"]]
	}`)))
	require.True(t, b.IsReady(), "successful snapshot after error recovers to ready")
	require.Equal(t, 50.0, b.Yes[40])
}

// TestApplyFrameToBooks_SnapshotErrorRejectsSubsequentDeltas is the
// core regression test for the orderbook state-machine bug found in
// the Phase 1 line audit. The sequence:
//
//  1. Send a malformed snapshot — Book exists in the map but is in
//     the uninitialized state.
//  2. Send 100 deltas — all must be rejected; book unchanged.
//  3. Send a valid snapshot — Book transitions back to ready.
//  4. Send 100 more deltas — all must apply; book reflects exactly
//     these and only these.
//
// Counter assertions: 100 rejected deltas, 1 snapshot error.
// Without the fix, the 100 pre-recovery deltas would mutate empty
// maps and produce phantom levels that don't exist in any real
// snapshot — exactly the silent divergence we're hardening against.
func TestApplyFrameToBooks_SnapshotErrorRejectsSubsequentDeltas(t *testing.T) {
	ResetCountersForTest()

	const ticker = "KXHIGHNY-26APR22-T64"
	books := map[string]*Book{}
	walls := map[string]int64{}

	// 1. Malformed snapshot — bad payload triggers the apply-error path.
	badSnapshot := KalshiFrame{
		Type: "orderbook_snapshot",
		Msg:  json.RawMessage(`{"market_ticker":"` + ticker + `","yes_dollars_fp":"not-an-array"}`),
	}
	malformed, err := ApplyFrameToBooks(books, walls, Envelope{Wall: 1}, badSnapshot)
	require.True(t, malformed, "malformed snapshot must be reported")
	require.ErrorIs(t, err, ErrSnapshotApplyFailed, "snapshot error must be a typed ErrSnapshotApplyFailed")
	require.NotNil(t, books[ticker], "book must exist in map even after a bad snapshot")
	require.False(t, books[ticker].IsReady(), "bad snapshot must leave book uninitialized")

	// 2. Send 100 deltas — every one must be rejected, book unchanged,
	//    rejected counter increments to 100.
	delta := KalshiFrame{
		Type: "orderbook_delta",
		Msg:  json.RawMessage(`{"market_ticker":"` + ticker + `","price_dollars":"0.4500","delta_fp":"1.00","side":"yes"}`),
	}
	for i := 0; i < 100; i++ {
		mf, e := ApplyFrameToBooks(books, walls, Envelope{Wall: int64(100 + i)}, delta)
		require.False(t, mf, "pre-snapshot delta is rejected, not malformed")
		require.ErrorIs(t, e, ErrBookUninitialized, "pre-snapshot delta must be rejected with ErrBookUninitialized (i=%d)", i)
	}
	require.Empty(t, books[ticker].Yes, "rejected deltas must NOT mutate the Yes map; this is the phantom-level bug")
	require.Empty(t, books[ticker].No, "rejected deltas must NOT mutate the No map; this is the phantom-level bug")
	require.False(t, books[ticker].IsReady(), "rejected deltas must not flip readiness")
	require.Equal(t, uint64(100), RejectedDeltasTotal(), "exactly 100 rejected deltas")

	// 3. Successful snapshot — book recovers to ready.
	goodSnapshot := KalshiFrame{
		Type: "orderbook_snapshot",
		Msg: json.RawMessage(`{
			"market_ticker":"` + ticker + `",
			"yes_dollars_fp":[["0.4500","100"]],
			"no_dollars_fp":[["0.5200","75"]]
		}`),
	}
	malformed, err = ApplyFrameToBooks(books, walls, Envelope{Wall: 1000}, goodSnapshot)
	require.False(t, malformed)
	require.NoError(t, err)
	require.True(t, books[ticker].IsReady(), "good snapshot must restore ready state")
	require.Equal(t, 100.0, books[ticker].Yes[45], "snapshot levels must be applied")
	require.Equal(t, 75.0, books[ticker].No[52], "snapshot levels must be applied")

	// 4. Send 100 more deltas — every one must apply against the
	//    real snapshot state. Final Yes[45] should be 100 (snapshot)
	//    + 100 (deltas) = 200; absolutely nothing should reflect the
	//    earlier rejected deltas.
	for i := 0; i < 100; i++ {
		mf, e := ApplyFrameToBooks(books, walls, Envelope{Wall: int64(2000 + i)}, delta)
		require.False(t, mf, "post-snapshot delta i=%d", i)
		require.NoError(t, e, "post-snapshot delta i=%d", i)
	}
	require.Equal(t, 200.0, books[ticker].Yes[45], "post-snapshot deltas must apply on top of snapshot baseline")
	require.True(t, books[ticker].IsReady(), "still ready after a stream of valid deltas")

	// Final counter check: 100 rejected (only from the pre-snapshot
	// window — counter did NOT advance during the post-snapshot
	// stream), 1 snapshot error.
	require.Equal(t, uint64(100), RejectedDeltasTotal(), "no further deltas should have been rejected after recovery")
	require.Equal(t, uint64(1), SnapshotErrorsTotal(), "exactly one snapshot error observed")
}

// TestApplyFrameToBooks_DeltaForUnknownTickerNotCounted documents
// that a delta for a ticker we've never seen (no book in the map)
// is a no-op, NOT a rejected-delta event. This matches the
// pre-existing "no subscription yet" semantics: edge-scan may
// receive frames before its book map has caught up; that's a
// schedule artifact, not a state-machine failure.
func TestApplyFrameToBooks_DeltaForUnknownTickerNotCounted(t *testing.T) {
	ResetCountersForTest()

	books := map[string]*Book{}
	walls := map[string]int64{}

	delta := KalshiFrame{
		Type: "orderbook_delta",
		Msg:  json.RawMessage(`{"market_ticker":"NEVER-SUBSCRIBED","price_dollars":"0.5","delta_fp":"5","side":"yes"}`),
	}
	malformed, err := ApplyFrameToBooks(books, walls, Envelope{Wall: 1}, delta)
	require.False(t, malformed)
	require.NoError(t, err, "stray delta for unknown ticker is a no-op, not a state-machine failure")
	require.Empty(t, books)
	require.Equal(t, uint64(0), RejectedDeltasTotal(), "unknown ticker must not bump rejected-delta counter")
	require.Equal(t, uint64(0), SnapshotErrorsTotal())
}
