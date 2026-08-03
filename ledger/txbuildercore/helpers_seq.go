package txbuildercore

import (
	"encoding/hex"
	"strconv"

	"github.com/lunfardo314/easyfl/engine"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/smallkv"
)

// FieldCmdCode is the conventional smallkv key carrying the 1-byte
// sequencer-request command code (0 = no-op, 1 = withdraw, 2 =
// set-seq-data, 3 = ask-stop-delegation). Wallet-side mirror of the
// constant in sequencer/txbuilder_seq.
const FieldCmdCode = byte(0)

// EnsureStopDelegationName is the canonical symbol for the constraint
// used at slot 4 of an ask-stop-delegation request output. The
// wallet pushes its compiled bytecode as the `extras...` arg to
// NewSequencerRequestOutput.
const EnsureStopDelegationName = "ensureStopDelegation"

// NewEnsureStopDelegationConstraint compiles
//
//	ensureStopDelegation(0x<chainID>, u64/<allowance>)
//
// for use at slot 4 of an ask-stop-delegation request output. allowance is
// the maximum the target sequencer may take out of the delegation balance
// as compensation; 0 leaves the delegation's non-decrease rule in force, so
// the whole compensation has to come from the request output itself.
func (l *Library[any]) NewEnsureStopDelegationConstraint(chainID base.ChainID, allowance uint64) ([]byte, error) {
	src := EnsureStopDelegationName + "(0x" + hex.EncodeToString(chainID[:]) + ", u64/" + strconv.FormatUint(allowance, 10) + ")"
	return l.CompileExpression(src)
}

// NewSequencerRequestOutput builds a tag-along output carrying a
// smallkv-encoded request payload at slot 3 and optional extra
// constraints at slots 4+. The wallet uses this for every "wallet →
// sequencer" command (withdraw, set-seq-data, ask-stop-delegation).
//
// The payload always has FieldCmdCode = requestCode prepended; any
// entries already in `params` are kept (a caller-supplied
// FieldCmdCode entry would be overwritten with `requestCode`). Pass
// nil for `params` to send a cmd code with no other fields.
//
// `extras` appends one constraint per byte slice at slots 4, 5, …
// in declaration order. ask-stop-delegation uses one extra (the
// ensureStopDelegation constraint).
func (l *Library[any]) NewSequencerRequestOutput(
	fee uint64,
	target base.ChainID,
	sender base.HolderID,
	requestCode byte,
	params *smallkv.Map,
	extras ...[]byte,
) (*Output, error) {
	tagAlongBin, err := l.lockBytecode(TagAlongLockName)
	if err != nil {
		return nil, err
	}

	// Build the request payload. Clone to avoid mutating the caller's
	// map; ensure the cmd code field is set last so it wins over any
	// caller-supplied entry at key 0.
	p := smallkv.New()
	if params != nil {
		c := params.Clone()
		// Copy every entry except key 0 (which we overwrite).
		// smallkv.Map doesn't expose iteration directly, but we can
		// reuse its Bytes/FromBytes round-trip to get a copy and then
		// set the cmd code.
		_ = c
		// Simpler: emit params bytes, parse into p, set cmd code last.
		p, err = smallkv.FromBytes(params.Bytes())
		if err != nil {
			return nil, err
		}
	}
	p.Set(FieldCmdCode, []byte{requestCode})

	b := NewOutputBuilder()
	b.PutConstraint(EncodeTokenBalance(fee), ConstraintIndexAmounts)
	b.PutConstraint(EncodeIndexValuesTuple([][]byte{sender[:], target[:]}), ConstraintIndexIndexValues)
	b.PutConstraint(tagAlongBin, ConstraintIndexLock)
	// slot 3 — inline-data wrapping of the smallkv payload.
	b.MustPushConstraint(engine.InlineDataBytecode(p.Bytes()))
	// slot 4+ — any extra constraints (e.g. ensureStopDelegation).
	for _, e := range extras {
		b.MustPushConstraint(e)
	}
	return b.Output(), nil
}
