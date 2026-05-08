package papertrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wh0o0pz/isobar/internal/openmeteo"
)

// midSpreadFromBids returns the (mid, spread) the trader would extract
// from a 1-deep two-sided book with the given yes-bid and no-bid prices.
// Mirrors replay.Book.MidYesCents/SpreadYesCents semantics so tests
// remain readable.
func midSpreadFromBids(yesBid, noBid int) (int, int) {
	yask := 100 - noBid
	mid := (yesBid + yask) / 2
	return mid, yask - yesBid
}

// constMembers returns 31 members all set to v.
func constMembers(v float64) []float64 {
	out := make([]float64, openmeteo.NumMembers)
	for i := range out {
		out[i] = v
	}
	return out
}

// makePoll builds a synthetic poll where every hour of the bracket day
// holds tempC at 25°C and every member sees the same trace. Daily
// max ≈ 77°F. NY UTC offset = -4 (April).
func makePoll(loc string, wallNanos int64, tempC float64) *openmeteo.EnsemblePoll {
	hours := 48
	times := make([]string, hours)
	temps := make([]float64, hours)
	for i := 0; i < hours; i++ {
		times[i] = time.Date(2026, 4, 22, 0, 0, 0, 0, time.UTC).
			Add(time.Duration(i) * time.Hour).Format("2006-01-02T15:04")
		temps[i] = tempC
	}
	p := &openmeteo.EnsemblePoll{
		Wall:     wallNanos,
		Location: loc,
		Times:    times,
	}
	for m := range p.Members {
		p.Members[m] = append([]float64{}, temps...)
	}
	return p
}

// Decision time: 2026-04-22 06:00 UTC = 02:00 NY local on 04-22, so the
// bracket day's settlement is ~22h out — comfortably above MinLeadHours.
var fixedDecidedAt = time.Date(2026, 4, 22, 6, 0, 0, 0, time.UTC).UnixNano()

func TestDecide_StrongYesEdge(t *testing.T) {
	// All members forecast 25°C ≈ 77°F. Bracket "high ≥ 70" → naive P=1.
	// Mid = 30¢, fee ≈ 4¢ → yes_edge ≈ +66¢ (way above 3¢ threshold).
	mid, spread := midSpreadFromBids(28, 70) // mid = (28 + (100-70))/2 = 29
	snap := Snapshot{
		Ticker:      "KXHIGHNY-26APR22-T70",
		MidCents:    mid,
		SpreadCents: spread,
		Poll:        makePoll("NY", fixedDecidedAt-int64(60*time.Second), 25),
		PollWall:    fixedDecidedAt - int64(60*time.Second),
		DecidedAt:   fixedDecidedAt,
	}
	bet, skip, ok := Decide(snap, TriggerDefaults(), nil)
	if !ok {
		t.Fatalf("expected bet to fire; got skip=%q", skip)
	}
	if bet.Side != SideYes {
		t.Errorf("side = %v, want yes", bet.Side)
	}
	if bet.EdgeCents < 50 {
		t.Errorf("edge = %v, want > 50", bet.EdgeCents)
	}
	if bet.SizingContracts != 1 {
		t.Errorf("sizing = %d, want 1", bet.SizingContracts)
	}
	if bet.SchemaVersion != SchemaVersion {
		t.Errorf("schema = %q, want %q", bet.SchemaVersion, SchemaVersion)
	}
}

func TestDecide_NoEdgeBelowThreshold(t *testing.T) {
	// Construct a sub-threshold edge that survives the corner filter.
	// All members at 25°C ≈ 77°F → naive=1 for "high ≥ 70". Mid=92 sits
	// inside [5,95]. Fee at mid=92: ceil(7·0.92·0.08)·2 = 2.
	// yes_edge = 100 − 92 − 2 = 6¢. Threshold=7 in this trigger pushes
	// it below; the trigger should reject for "no side clears threshold"
	// rather than for being in a corner.
	mid, spread := midSpreadFromBids(91, 5) // mid = (91 + 95)/2 = 93 — inside [5,95]
	snap := Snapshot{
		Ticker:      "KXHIGHNY-26APR22-T70",
		MidCents:    mid,
		SpreadCents: spread,
		Poll:        makePoll("NY", fixedDecidedAt, 25),
		PollWall:    fixedDecidedAt,
		DecidedAt:   fixedDecidedAt,
	}
	trig := TriggerDefaults()
	trig.MinEdgeCents = 7 // push the 5–6¢ yes_edge below threshold
	_, skip, ok := Decide(snap, trig, nil)
	if ok {
		t.Errorf("expected skip when edge < threshold; got ok")
	}
	if !strings.Contains(skip, "threshold") {
		t.Errorf("skip reason = %q, want 'threshold' mention", skip)
	}
}

func TestDecide_SkipsOneSidedBook(t *testing.T) {
	// One-sided book → MidYesCents() returns 0; we model that by passing
	// 0 directly. Decide rejects mid <= 0 OR mid >= 100.
	snap := Snapshot{
		Ticker:      "KXHIGHNY-26APR22-T70",
		MidCents:    0,
		SpreadCents: 100,
		Poll:        makePoll("NY", fixedDecidedAt, 25),
		PollWall:    fixedDecidedAt,
		DecidedAt:   fixedDecidedAt,
	}
	_, skip, ok := Decide(snap, TriggerDefaults(), nil)
	if ok {
		t.Errorf("expected skip on one-sided book")
	}
	if !strings.Contains(skip, "one-sided") {
		t.Errorf("skip = %q", skip)
	}
}

func TestDecide_SkipsLowerCornerMid(t *testing.T) {
	// Mid=2 is below the 5¢ default floor — corner-of-the-book bet,
	// unfillable in practice, must reject before edge math runs.
	mid, spread := midSpreadFromBids(1, 97) // mid = (1 + 3)/2 = 2
	snap := Snapshot{
		Ticker:      "KXHIGHNY-26APR22-T70",
		MidCents:    mid,
		SpreadCents: spread,
		Poll:        makePoll("NY", fixedDecidedAt, 25),
		PollWall:    fixedDecidedAt,
		DecidedAt:   fixedDecidedAt,
	}
	_, skip, ok := Decide(snap, TriggerDefaults(), nil)
	if ok {
		t.Errorf("expected skip on lower-corner mid")
	}
	if !strings.Contains(skip, "lower corner") {
		t.Errorf("skip = %q, want 'lower corner' mention", skip)
	}
}

func TestDecide_SkipsUpperCornerMid(t *testing.T) {
	// Mid=98 is above the 95¢ default ceiling — symmetric corner.
	mid, spread := midSpreadFromBids(97, 1) // mid = (97 + 99)/2 = 98
	snap := Snapshot{
		Ticker:      "KXHIGHNY-26APR22-T70",
		MidCents:    mid,
		SpreadCents: spread,
		Poll:        makePoll("NY", fixedDecidedAt, 25),
		PollWall:    fixedDecidedAt,
		DecidedAt:   fixedDecidedAt,
	}
	_, skip, ok := Decide(snap, TriggerDefaults(), nil)
	if ok {
		t.Errorf("expected skip on upper-corner mid")
	}
	if !strings.Contains(skip, "upper corner") {
		t.Errorf("skip = %q, want 'upper corner' mention", skip)
	}
}

func TestDecide_CornerFilterDisabledByZero(t *testing.T) {
	// MinMidCents=0 disables the lower bound (still rejects mid<=0 via
	// the one-sided guard). Use a permissive trigger and verify a
	// mid=2 snapshot now passes the corner check.
	mid, spread := midSpreadFromBids(1, 97) // mid=2
	snap := Snapshot{
		Ticker:      "KXHIGHNY-26APR22-T70",
		MidCents:    mid,
		SpreadCents: spread,
		Poll:        makePoll("NY", fixedDecidedAt, 25),
		PollWall:    fixedDecidedAt,
		DecidedAt:   fixedDecidedAt,
	}
	trig := TriggerDefaults()
	trig.MinMidCents = 0
	trig.MaxMidCents = 0
	trig.MaxSpreadCents = 0 // would otherwise reject — wide synthetic book
	_, skip, _ := Decide(snap, trig, nil)
	// The corner-filter must not be the rejection reason now; it can
	// still reject for other reasons (here the mid=2 + naive=1.0 is
	// such a strong yes-edge that the bet WILL fire).
	if strings.Contains(skip, "corner") {
		t.Errorf("corner filter should be off; got %q", skip)
	}
}

func TestDecide_SkipsWideSpread(t *testing.T) {
	// yes_bid=20, no_bid=20 → yes_ask=80, spread=60. Way above 5¢ default.
	mid, spread := midSpreadFromBids(20, 20)
	snap := Snapshot{
		Ticker:      "KXHIGHNY-26APR22-T70",
		MidCents:    mid,
		SpreadCents: spread,
		Poll:        makePoll("NY", fixedDecidedAt, 25),
		PollWall:    fixedDecidedAt,
		DecidedAt:   fixedDecidedAt,
	}
	_, skip, ok := Decide(snap, TriggerDefaults(), nil)
	if ok {
		t.Errorf("expected skip on wide spread")
	}
	if !strings.Contains(skip, "spread") {
		t.Errorf("skip = %q, want 'spread' mention", skip)
	}
}

func TestDecide_SkipsStalePoll(t *testing.T) {
	// 5h old poll, default MaxPollAge = 4h.
	old := fixedDecidedAt - int64(5*time.Hour)
	mid, spread := midSpreadFromBids(28, 70)
	snap := Snapshot{
		Ticker:      "KXHIGHNY-26APR22-T70",
		MidCents:    mid,
		SpreadCents: spread,
		Poll:        makePoll("NY", old, 25),
		PollWall:    old,
		DecidedAt:   fixedDecidedAt,
	}
	_, skip, ok := Decide(snap, TriggerDefaults(), nil)
	if ok {
		t.Errorf("expected skip on stale poll")
	}
	if !strings.Contains(skip, "stale") {
		t.Errorf("skip = %q, want 'stale' mention", skip)
	}
}

func TestDecide_SkipsNearSettlement(t *testing.T) {
	// Decision = 04-23 03:00 UTC = 23:00 NY-local 04-22. Settlement at
	// 04-23 00:00 NY-local = 04-23 04:00 UTC. Lead = ~1h < 2h floor.
	tooLate := time.Date(2026, 4, 23, 3, 0, 0, 0, time.UTC).UnixNano()
	mid, spread := midSpreadFromBids(28, 70)
	snap := Snapshot{
		Ticker:      "KXHIGHNY-26APR22-T70",
		MidCents:    mid,
		SpreadCents: spread,
		Poll:        makePoll("NY", tooLate, 25),
		PollWall:    tooLate,
		DecidedAt:   tooLate,
	}
	_, skip, ok := Decide(snap, TriggerDefaults(), nil)
	if ok {
		t.Errorf("expected skip near settlement")
	}
	if !strings.Contains(skip, "settlement") {
		t.Errorf("skip = %q, want 'settlement' mention", skip)
	}
}

func TestDecide_UnparseableTicker(t *testing.T) {
	mid, spread := midSpreadFromBids(45, 55)
	_, skip, ok := Decide(Snapshot{
		Ticker:      "NOT-A-TICKER",
		MidCents:    mid,
		SpreadCents: spread,
	}, TriggerDefaults(), nil)
	if ok {
		t.Errorf("expected skip on bad ticker")
	}
	if !strings.Contains(skip, "ticker") {
		t.Errorf("skip = %q", skip)
	}
}

func TestLedger_RecordDedupes(t *testing.T) {
	dir := t.TempDir()
	led, err := OpenLedger(LedgerOptions{
		Dir:        dir,
		FlushEvery: 10 * time.Millisecond,
		QueueSize:  16,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer led.Close()

	bet := Bet{
		SchemaVersion: SchemaVersion,
		ID:            "KXHIGHNY-26APR22-T70|yes|2026-04-22",
		Ticker:        "KXHIGHNY-26APR22-T70",
		Side:          SideYes,
	}
	wrote, err := led.Record(bet)
	if err != nil || !wrote {
		t.Fatalf("first Record: wrote=%v err=%v", wrote, err)
	}
	wrote, err = led.Record(bet)
	if err != nil {
		t.Fatalf("second Record err: %v", err)
	}
	if wrote {
		t.Errorf("expected dedup hit; got wrote=true")
	}
	if got := led.Count(); got != 1 {
		t.Errorf("Count = %d, want 1", got)
	}
}

func TestLedger_MarkRecoveredSkipsRewrite(t *testing.T) {
	dir := t.TempDir()
	led, err := OpenLedger(LedgerOptions{
		Dir:        dir,
		FlushEvery: 10 * time.Millisecond,
		QueueSize:  16,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer led.Close()

	id := "KXHIGHNY-26APR22-T70|yes|2026-04-22"
	led.MarkRecorded(id)
	wrote, _ := led.Record(Bet{ID: id})
	if wrote {
		t.Errorf("expected skip on recovered ID")
	}
}

func TestLedger_RecoverDedupesAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	// Session 1: write two bets, one for tomorrow (should recover) and
	// one for last week (should be ignored — already settled).
	led1, err := OpenLedger(LedgerOptions{
		Dir:        dir,
		FlushEvery: 5 * time.Millisecond,
		QueueSize:  16,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	betFuture := Bet{
		SchemaVersion:      SchemaVersion,
		ID:                 "K|yes|2026-04-22",
		Ticker:             "K",
		Side:               SideYes,
		SettlementUnixNano: now.AddDate(0, 0, 2).UnixNano(),
	}
	betPast := Bet{
		SchemaVersion:      SchemaVersion,
		ID:                 "K|yes|2026-01-01",
		Ticker:             "K",
		Side:               SideYes,
		SettlementUnixNano: now.AddDate(0, 0, -7).UnixNano(),
	}
	if _, err := led1.Record(betFuture); err != nil {
		t.Fatal(err)
	}
	if _, err := led1.Record(betPast); err != nil {
		t.Fatal(err)
	}
	if err := led1.Close(); err != nil {
		t.Fatal(err)
	}

	// Session 2: open a fresh ledger over the same dir, recover, and
	// verify the future bet dedups while a non-recorded one writes.
	led2, err := OpenLedger(LedgerOptions{
		Dir:        dir,
		FlushEvery: 5 * time.Millisecond,
		QueueSize:  16,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer led2.Close()

	restored, err := led2.Recover(dir, now)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if restored != 1 {
		t.Errorf("restored = %d, want 1 (the future bet only)", restored)
	}
	if c := led2.Count(); c != 1 {
		t.Errorf("Count = %d, want 1", c)
	}

	// Re-recording the future bet must dedup; a new ID must write.
	wrote, _ := led2.Record(betFuture)
	if wrote {
		t.Errorf("expected dedup hit on recovered future bet")
	}
	wrote, _ = led2.Record(Bet{
		SchemaVersion:      SchemaVersion,
		ID:                 "K|no|2026-04-22",
		Ticker:             "K",
		Side:               SideNo,
		SettlementUnixNano: now.AddDate(0, 0, 2).UnixNano(),
	})
	if !wrote {
		t.Errorf("expected new ID to write")
	}
}

func TestLedger_RecoverHandlesMissingDir(t *testing.T) {
	// First-run scenario: no papertrade dir yet. Recover must succeed
	// silently, not fail, so the binary can boot cleanly.
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")
	led, err := OpenLedger(LedgerOptions{
		Dir:        dir,
		FlushEvery: 5 * time.Millisecond,
		QueueSize:  16,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer led.Close()

	restored, err := led.Recover(missing, time.Now())
	if err != nil {
		t.Errorf("expected nil err on missing dir, got %v", err)
	}
	if restored != 0 {
		t.Errorf("restored = %d, want 0", restored)
	}
}

func TestClassifyResolution(t *testing.T) {
	cases := []struct {
		status, result string
		want           Resolution
	}{
		{"settled", "yes", ResolutionYes},
		{"settled", "no", ResolutionNo},
		{"finalized", "yes", ResolutionYes},
		{"expired", "no", ResolutionNo},
		{"settled", "", ResolutionVoid},
		// case + whitespace tolerance
		{"SETTLED", " Yes ", ResolutionYes},
		// in-flight markets
		{"open", "", ResolutionUnsettled},
		{"active", "", ResolutionUnsettled},
		{"", "", ResolutionUnsettled},
		// unknown status → conservatively unsettled (caller retries)
		{"weird-state", "yes", ResolutionUnsettled},
		// settled with unknown result → defensive void
		{"settled", "maybe", ResolutionVoid},
	}
	for _, c := range cases {
		got := ClassifyResolution(c.status, c.result)
		if got != c.want {
			t.Errorf("ClassifyResolution(%q, %q) = %q, want %q",
				c.status, c.result, got, c.want)
		}
	}
}

func TestPnLFromBet(t *testing.T) {
	b := Bet{
		Side:              SideYes,
		MidCents:          30,
		FeeRoundTripCents: 4,
	}
	cases := []struct {
		name     string
		side     Side
		mid      int
		fee      int
		res      Resolution
		wantWon  bool
		wantPnL  int
	}{
		{"yes-bet-yes-resolves-win", SideYes, 30, 4, ResolutionYes, true, 100 - 30 - 4},
		{"yes-bet-no-resolves-lose", SideYes, 30, 4, ResolutionNo, false, -30 - 4},
		{"no-bet-no-resolves-win", SideNo, 60, 4, ResolutionNo, true, 100 - 60 - 4},
		{"no-bet-yes-resolves-lose", SideNo, 60, 4, ResolutionYes, false, -60 - 4},
		{"void-eats-fee-only", SideYes, 30, 4, ResolutionVoid, false, -4},
		{"unsettled-no-pnl", SideYes, 30, 4, ResolutionUnsettled, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b.Side = tc.side
			b.MidCents = tc.mid
			b.FeeRoundTripCents = tc.fee
			won, pnl := PnLFromBet(b, tc.res)
			if won != tc.wantWon {
				t.Errorf("won = %v, want %v", won, tc.wantWon)
			}
			if pnl != tc.wantPnL {
				t.Errorf("pnl = %v, want %v", pnl, tc.wantPnL)
			}
		})
	}
}

func TestIterBetsSkipsOldDates(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	led, err := OpenLedger(LedgerOptions{
		Dir: dir, FlushEvery: 5 * time.Millisecond, QueueSize: 16,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Two bets — the journal will write them under today's UTC date dir.
	for i, id := range []string{"K|yes|2026-04-22", "K|no|2026-04-22"} {
		_, _ = led.Record(Bet{
			SchemaVersion: SchemaVersion, ID: id, Ticker: "K",
			Side:               Side([]string{"yes", "no"}[i]),
			SettlementUnixNano: now.AddDate(0, 0, 2).UnixNano(),
		})
	}
	if err := led.Close(); err != nil {
		t.Fatal(err)
	}

	count := 0
	if err := IterBets(dir, now, func(b Bet) error {
		count++
		if b.Ticker != "K" {
			t.Errorf("unexpected ticker %q", b.Ticker)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("iter count = %d, want 2", count)
	}

	// since = far future → cutoff > today → no dirs match.
	count = 0
	if err := IterBets(dir, now.AddDate(0, 0, 30), func(_ Bet) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("future-since count = %d, want 0", count)
	}
}

func TestAppendAndIterOutcomes(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	dateDir := now.UTC().Format("2006-01-02")
	path := filepath.Join(dir, dateDir, "outcomes-test.jsonl")

	o1 := Outcome{
		SchemaVersion: OutcomeSchemaVersion,
		BetID:         "K|yes|2026-04-22",
		Ticker:        "K",
		Resolution:    ResolutionYes,
		BetSide:       SideYes,
		Won:           true,
		PnLCents:      66,
	}
	o2 := o1
	o2.BetID = "K|no|2026-04-22"
	o2.BetSide = SideNo
	o2.Won = false
	o2.PnLCents = -34

	if _, err := AppendOutcome(path, o1); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendOutcome(path, o2); err != nil {
		t.Fatal(err)
	}

	seen := map[string]Outcome{}
	if err := IterOutcomes(dir, now, func(o Outcome) error {
		seen[o.BetID] = o
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("seen %d, want 2: %v", len(seen), seen)
	}
	if seen[o1.BetID].PnLCents != 66 {
		t.Errorf("o1 pnl = %d, want 66", seen[o1.BetID].PnLCents)
	}
	if !seen[o2.BetID].Won == false { // double-negate intentionally for clarity
		t.Errorf("o2 should be lost")
	}
}

func TestLedger_PersistsToJSONL(t *testing.T) {
	dir := t.TempDir()
	led, err := OpenLedger(LedgerOptions{
		Dir:        dir,
		FlushEvery: 5 * time.Millisecond,
		QueueSize:  16,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	bet := Bet{
		SchemaVersion: SchemaVersion,
		ID:            "K|yes|2026-04-22",
		Ticker:        "K",
		Side:          SideYes,
		EdgeCents:     5.0,
	}
	if _, err := led.Record(bet); err != nil {
		t.Fatal(err)
	}
	if err := led.Close(); err != nil {
		t.Fatal(err)
	}

	// Find the .jsonl file and decode the bet line. The journal nests
	// files under <dir>/<YYYY-MM-DD>/papertrade-*.jsonl, so walk one
	// level down into the date directory before looking for the file.
	var jsonlPath string
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		if strings.HasSuffix(path, ".jsonl") && jsonlPath == "" {
			jsonlPath = path
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if jsonlPath == "" {
		t.Fatalf("no .jsonl file under %s", dir)
	}
	contents, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(contents)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d:\n%s", len(lines), contents)
	}

	// Each journal line is an envelope with the bet payload nested inside.
	var env struct {
		Source  string          `json:"src"`
		Kind    string          `json:"k"`
		Payload json.RawMessage `json:"p"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &env); err != nil {
		t.Fatal(err)
	}
	if env.Source != "papertrade" {
		t.Errorf("src = %q, want papertrade", env.Source)
	}
	if env.Kind != "bet" {
		t.Errorf("kind = %q, want bet", env.Kind)
	}
	var got Bet
	if err := json.Unmarshal(env.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != bet.ID {
		t.Errorf("payload ID = %q, want %q", got.ID, bet.ID)
	}
	if got.EdgeCents != 5.0 {
		t.Errorf("edge = %v, want 5", got.EdgeCents)
	}
}
