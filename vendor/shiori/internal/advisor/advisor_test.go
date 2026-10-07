package advisor

import "testing"

func TestParseThresholds(t *testing.T) {
	th, err := ParseThresholds("min-savings-kib=8,notes=10,terminal-percent=30,plan-kib=64,keep-notes=5")
	if err != nil {
		t.Fatal(err)
	}
	if *th != (Thresholds{MinSavingsBytes: 8 << 10, Notes: 10, TerminalPercent: 30, PlanBytes: 64 << 10, KeepNotes: 5}) {
		t.Fatalf("%+v", *th)
	}
	if th, _ := ParseThresholds("off"); !th.Off {
		t.Fatal("off")
	}
	for _, bad := range []string{"", "notes", "notes=0", "notes=x", "terminal-percent=101", "keep-notes=10001", "size=1"} {
		if _, err := ParseThresholds(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
