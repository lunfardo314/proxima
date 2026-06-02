package dag_explorer

import "testing"

// Verifies that parseShortTxID accepts the dashed short form a user is likely to
// paste from logs / CLI output: bare slot, slot-tick, slot-tick-hashPrefix, the
// optional leading 's' (sequencer marker), and any trailing ".." or "#<idx>" decoration.
func TestParseShortTxID(t *testing.T) {
	cases := []struct {
		in        string
		wantSlot  uint32
		wantTick  byte // 255 = "any tick"
		wantHash  string
		wantOk    bool
	}{
		// bare slot
		{"220942", 220942, 255, "", true},

		// slot-tick (non-sequencer)
		{"220942-36", 220942, 36, "", true},

		// slot-tick-hashPrefix
		{"220942-36-0066f7", 220942, 36, "0066f7", true},

		// 's' sequencer marker is informational, must not affect lookup
		{"s220942-36-0066f7", 220942, 36, "0066f7", true},

		// branch tx: tick=0, 's' prefix
		{"s23-0-011ec4f1be45", 23, 0, "011ec4f1be45", true},

		// short / very-short suffix ".." must be stripped from the hash prefix
		{"s220942-36-0066f7..", 220942, 36, "0066f7", true},

		// output-id "#<idx>" suffix must be stripped from the hash prefix
		{"s220942-36-0066f7#2", 220942, 36, "0066f7", true},
		{"s220942-36-0066f7..#2", 220942, 36, "0066f7", true},

		// leading/trailing whitespace tolerated
		{"  220942-36-0066f7  ", 220942, 36, "0066f7", true},

		// not parseable
		{"", 0, 0, "", false},
		{"abc", 0, 0, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			slot, tick, hash, ok := parseShortTxID(tc.in)
			if ok != tc.wantOk {
				t.Fatalf("ok mismatch: got %v want %v", ok, tc.wantOk)
			}
			if !ok {
				return
			}
			if slot != tc.wantSlot || tick != tc.wantTick || hash != tc.wantHash {
				t.Fatalf("got (slot=%d tick=%d hash=%q), want (slot=%d tick=%d hash=%q)",
					slot, tick, hash, tc.wantSlot, tc.wantTick, tc.wantHash)
			}
		})
	}
}
