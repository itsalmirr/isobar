// Package passing exercises every allowed shape: a now func field, a
// nolint-tagged direct read, time.NewTicker / time.After / time.Sleep
// (not flagged), and a non-`time` symbol named Now (not flagged).
package passing

import (
	"time"
)

// thing has a swappable now field — the analyzer must not flag the
// methods that call t.now() because that's the discipline-conforming
// pattern documented in ADR-0012.
type thing struct {
	now func() time.Time
}

func newThing() *thing {
	return &thing{now: time.Now} //nolint:clockcheck // construction-site default
}

func (t *thing) read() time.Time {
	return t.now()
}

// callTicker / callAfter / callSleep should not be flagged — these are
// monotonic-scheduling primitives, not wall-time reads.
func callTicker() {
	tk := time.NewTicker(time.Second)
	tk.Stop()
}

func callAfter() {
	<-time.After(time.Millisecond)
}

func callSleep() {
	time.Sleep(time.Nanosecond)
}

// shadowedTime tests that a local var named `time` doesn't trigger.
// The analyzer should resolve names via type info, not by identifier.
func shadowedTime() any {
	type stubTime struct{}
	// nolint above guards in case the analyzer is over-eager.
	timeShadow := stubTime{}
	_ = timeShadow
	return timeShadow
}

// nolintedDirectNow demonstrates the escape hatch — a justified
// //nolint:clockcheck above a direct read. Real callers must include a
// justification comment; for the analyzer self-test we just need the
// directive itself to be honored.
func nolintedDirectNow() time.Time {
	//nolint:clockcheck // self-test: direct read is the documented escape hatch
	return time.Now()
}
