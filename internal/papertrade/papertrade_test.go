package papertrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wh0o0pz/isobar/internal/openmeteo"
	"github.com/wh0o0pz/isobar/internal/replay"
)

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

func mockBook(yesBid, noBid int) *replay.Book {
	b := replay.NewBook()
	b.Yes[yesBid] = 100
	b.No[noBid] = 100
	return b
}

// Decision time: 2026-04-22 06:00 UTC = 02:00 NY local on 04-22, so the
// bracket day's settlement is ~22h out — comfortably above MinLeadHours.
var fixedDecidedAt = time.Date(2026, 4, 22, 6, 0, 0, 0, time.UTC).UnixNano()

func TestDecide_StrongYesEdge(t *testing.T) {
	// All members forecast 25°C ≈ 77°F. Bracket "high ≥ 70" → naive P=1.
	// Mid = 30¢, fee ≈ 4¢ → yes_edge ≈ +66¢ (way above 3¢ threshold).
	snap := Snapshot{
		Ticker:    "KXHIGHNY-26APR22-T70",
		Book:      mockBook(28, 70), // mid = (28 + (100-70))/2 = 29
		Poll:      makePoll("NY", fixedDecidedAt-int64(60*time.Second), 25),
		PollWall:  fixedDecidedAt - int64(60*time.Second),
		DecidedAt: fixedDecidedAt,
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
	// All members forecast 25°C ≈ 77°F. Bracket "high ≥ 70". Naive P=1,
	// mid=95 → yes_edge = 100-95-fee, no_edge = 95-100-fee = -negative.
	// At mid=95, fee = ceil(7*0.95*0.05)*2 = ceil(0.33)*2 = 2.
	// yes_edge = 100-95-2 = 3 — exactly at threshold, should fire.
	// Bump mid to 97 to push edge below threshold.
	snap := Snapshot{
		Ticker:    "KXHIGHNY-26APR22-T70",
		Book:      mockBook(96, 2), // mid=97
		Poll:      makePoll("NY", fixedDecidedAt, 25),
		PollWall:  fixedDecidedAt,
		DecidedAt: fixedDecidedAt,
	}
	_, skip, ok := Decide(snap, TriggerDefaults(), nil)
	if ok {
		t.Errorf("expected skip when edge < threshold; got ok")
	}
	if !strings.Contains(skip, "threshold") {
		t.Errorf("skip reason = %q, want 'threshold' mention", skip)
	}
}

func TestDecide_SkipsOneSidedBook(t *testing.T) {
	b := replay.NewBook()
	b.Yes[45] = 100
	snap := Snapshot{
		Ticker:    "KXHIGHNY-26APR22-T70",
		Book:      b,
		Poll:      makePoll("NY", fixedDecidedAt, 25),
		PollWall:  fixedDecidedAt,
		DecidedAt: fixedDecidedAt,
	}
	_, skip, ok := Decide(snap, TriggerDefaults(), nil)
	if ok {
		t.Errorf("expected skip on one-sided book")
	}
	if !strings.Contains(skip, "one-sided") {
		t.Errorf("skip = %q", skip)
	}
}

func TestDecide_SkipsWideSpread(t *testing.T) {
	// yes_bid=20, no_bid=20 → yes_ask=80, spread=60. Way above 5¢ default.
	snap := Snapshot{
		Ticker:    "KXHIGHNY-26APR22-T70",
		Book:      mockBook(20, 20),
		Poll:      makePoll("NY", fixedDecidedAt, 25),
		PollWall:  fixedDecidedAt,
		DecidedAt: fixedDecidedAt,
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
	snap := Snapshot{
		Ticker:    "KXHIGHNY-26APR22-T70",
		Book:      mockBook(28, 70),
		Poll:      makePoll("NY", old, 25),
		PollWall:  old,
		DecidedAt: fixedDecidedAt,
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
	// Decision time = 04-22 23:30 UTC = 19:30 NY-local on 04-22. Settlement
	// = 04-23 00:00 NY-local = 04-23 04:00 UTC. Lead = ~4.5h... not skipped.
	// Move decision to 04-23 01:00 UTC = 21:00 NY-local 04-22 → lead ~3h,
	// still above 2h. Move to 04-23 03:00 UTC = 23:00 NY-local 04-22 →
	// lead ~1h, BELOW 2h, expect skip.
	tooLate := time.Date(2026, 4, 23, 3, 0, 0, 0, time.UTC).UnixNano()
	snap := Snapshot{
		Ticker:    "KXHIGHNY-26APR22-T70",
		Book:      mockBook(28, 70),
		Poll:      makePoll("NY", tooLate, 25),
		PollWall:  tooLate,
		DecidedAt: tooLate,
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
	_, skip, ok := Decide(Snapshot{
		Ticker: "NOT-A-TICKER",
		Book:   mockBook(45, 55),
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
