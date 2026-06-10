package universe

import "testing"

func gateGroups() []Group {
	return []Group{
		{Event: Event{EventTicker: "KXHIGHNY-26JUN10", SeriesTicker: "KXHIGHNY"}},
		{Event: Event{EventTicker: "KXBTCD-26JUN10", SeriesTicker: "KXBTCD"}},
		{Event: Event{EventTicker: "KXLOWTNYC-26JUN10", SeriesTicker: "KXLOWTNYC"}},
	}
}

func TestGateFailsClosed(t *testing.T) {
	kept, dropped := NewGate(nil).Filter(gateGroups())
	if len(kept) != 0 {
		t.Fatalf("empty gate kept %d groups; eligibility must fail closed", len(kept))
	}
	if len(dropped) != 3 {
		t.Fatalf("dropped = %d, want 3", len(dropped))
	}
	for _, d := range dropped {
		if d.Reason == "" {
			t.Errorf("drop %s has empty reason; report needs auditability", d.EventTicker)
		}
	}
}

func TestGateKeepsOnlyConfirmed(t *testing.T) {
	g := NewGate([]string{"KXHIGHNY", "KXLOWTNYC", ""})
	kept, dropped := g.Filter(gateGroups())
	if len(kept) != 2 {
		t.Fatalf("kept = %d, want 2", len(kept))
	}
	for _, k := range kept {
		if k.Event.SeriesTicker == "KXBTCD" {
			t.Fatal("unconfirmed series passed the gate")
		}
	}
	if len(dropped) != 1 || dropped[0].SeriesTicker != "KXBTCD" {
		t.Fatalf("dropped = %+v, want exactly KXBTCD", dropped)
	}
	if g.Allowed("") {
		t.Fatal("empty series ticker must never be allowed")
	}
}
