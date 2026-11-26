package ledger

import "testing"

// A structurally valid output whose lock has a known prefix but cannot be decoded (here a
// delegation lock with malformed arguments) must not crash the readers. Such bytes can reach a
// wallet from a node without full validation; readers return a placeholder lock instead.
func TestOutputLockUnparseableNoPanic(t *testing.T) {
	InitWithTestingLedgerData()
	data := []byte("@\x05\x04\x00000\x00\bFJ000000\x040000\x040000")
	o, err := OutputFromBytes(data)
	if err != nil {
		t.Fatalf("structural parse is expected to accept the output: %v", err)
	}
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("reader panicked: %v", p)
		}
	}()
	if name := o.Lock().Name(); name != "unparseableLock" {
		t.Fatalf("expected the placeholder lock, got %s", name)
	}
	_ = o.LinesHR().String()
	if _, ok := o.StemLock(); ok {
		t.Fatalf("placeholder must not read as a stem lock")
	}
	if o.DelegationLock() != nil {
		t.Fatalf("placeholder must not read as a delegation lock")
	}
	_ = o.TagAlongLock()

	// full validation still rejects the output
	if _, err = OutputFromBytes(data, WithFullValidation()); err == nil {
		t.Fatalf("full validation must reject the unparseable lock")
	}
}
