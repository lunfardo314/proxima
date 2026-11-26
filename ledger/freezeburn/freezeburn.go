// Package freezeburn holds the node side of library upgrade 8 (kb/burn_frozen.md): the
// outputs still frozen after the 2026-11-17 exploit and the keys that moved its funds. Until
// the upgrade slot it freezes them, as the node blocklist did; at the slot the crossing
// branch, the first of each lineage at or after it, deletes the outputs from its own
// baseline as it is committed, and its stem declares a supply lower by their total. The
// library commits to the list by its hash and total (ledger/def/upgrade8.easyfl).
package freezeburn

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/blocklist"
	"golang.org/x/crypto/blake2b"
)

var (
	burnedIDs     []base.OutputID // sorted
	burnedAmounts map[base.OutputID]uint64
	burnedTotal   uint64
	frozenHolders = make(map[base.HolderID]struct{}, len(exploitHolders))
)

func init() {
	amounts := make(map[base.OutputID]uint64, len(burnedOutputs))
	for _, o := range burnedOutputs {
		oid, err := base.OutputIDFromHexString(o.ID)
		if err != nil {
			panic(fmt.Errorf("freezeburn: output %q: %w", o.ID, err))
		}
		amounts[oid] = o.Amount
	}
	if len(amounts) != len(burnedOutputs) {
		panic("freezeburn: the list holds a duplicate")
	}
	setBurned(amounts)
	for _, s := range exploitHolders {
		b, err := hex.DecodeString(s)
		if err != nil || len(b) != len(base.HolderID{}) {
			panic(fmt.Errorf("freezeburn: holder %q is not a 32-byte hex", s))
		}
		frozenHolders[base.HolderID(b)] = struct{}{}
	}
}

func setBurned(amounts map[base.OutputID]uint64) {
	burnedAmounts = amounts
	burnedIDs = burnedIDs[:0]
	burnedTotal = 0
	for oid, a := range amounts {
		burnedIDs = append(burnedIDs, oid)
		burnedTotal += a
	}
	slices.SortFunc(burnedIDs, func(a, b base.OutputID) int { return bytes.Compare(a[:], b[:]) })
}

// SetBurnedForTesting replaces the burned outputs and returns the function that restores
// them, for t.Cleanup; for tests only, whose ledgers do not hold the real ones.
func SetBurnedForTesting(amounts map[base.OutputID]uint64) (restore func()) {
	saved := burnedAmounts
	setBurned(amounts)
	return func() { setBurned(saved) }
}

// BurnedAmount is the total of the burned outputs, in motes: the library constant
// constBurnedAmount.
func BurnedAmount() uint64 {
	return burnedTotal
}

// ListHash is blake2b-256 of the sorted burned output IDs, the library constant
// constBurnedListHash.
func ListHash() [32]byte {
	var buf bytes.Buffer
	for _, oid := range burnedIDs {
		buf.Write(oid[:])
	}
	return blake2b.Sum256(buf.Bytes())
}

// BurnedOutputIDs returns the burned output IDs, sorted.
func BurnedOutputIDs() []base.OutputID {
	return slices.Clone(burnedIDs)
}

// CheckInputs refuses a transaction consuming a burned output, at every slot: each was
// unspent when the list was cut and frozen since, and from the upgrade slot it no longer
// exists. The error wraps blocklist.ErrFrozen, so the input gate treats it as a policy refusal.
func CheckInputs(inputs []base.OutputID) error {
	for _, oid := range inputs {
		if _, found := burnedAmounts[oid]; found {
			return fmt.Errorf("%w: input %s is burned", blocklist.ErrFrozen, oid.StringHex())
		}
	}
	return nil
}

// CheckSigner refuses a transaction of the given slot signed by an exploit key, from
// blocklist.ActivationSlot until upgrade 8 applies; from then on the keys hold nothing.
func CheckSigner(slot uint32, signer base.HolderID) error {
	if slot < blocklist.ActivationSlot || ledger.L(slot).HasUpgrade8() {
		return nil
	}
	if _, found := frozenHolders[signer]; found {
		return fmt.Errorf("%w: signer %s is frozen", blocklist.ErrFrozen, hex.EncodeToString(signer[:]))
	}
	return nil
}

// IsCrossing tells whether a branch, or any milestone, of slot whose baseline is of
// baselineSlot crosses upgrade 8: the library of its slot carries the upgrade and that of
// its baseline does not. On a ledger whose genesis folds upgrade 8 nothing crosses.
func IsCrossing(baselineSlot, slot uint32) bool {
	return ledger.L(slot).HasUpgrade8() && !ledger.L(baselineSlot).HasUpgrade8()
}

// BurnedAt is the amount a transaction of slot on a baseline of baselineSlot subtracts from
// the baseline supply: BurnedAmount() when it crosses upgrade 8, zero otherwise.
func BurnedAt(baselineSlot, slot uint32) uint64 {
	if IsCrossing(baselineSlot, slot) {
		return burnedTotal
	}
	return 0
}

// MissingFromBaseline returns the first burned output a baseline lacks, if any. A crossing
// branch on such a baseline is invalid: its lineage spent a frozen output.
func MissingFromBaseline(has func(base.OutputID) bool) (base.OutputID, bool) {
	for _, oid := range burnedIDs {
		if !has(oid) {
			return oid, true
		}
	}
	return base.OutputID{}, false
}

// Mutations is where DeleteBurned writes: the mutations of a branch.
type Mutations interface {
	InsertDelOutputMutation(oid base.OutputID)
	InsertDelChainMutation(id base.ChainID)
}

// DeleteBurned adds to the mutations of a crossing branch the deletion of every burned output
// present in its baseline, read by get, and of the chain record of any of them that carries a
// chain, as the end of a chain does. It returns the amount deleted, BurnedAmount() on any
// baseline its attacher accepted.
func DeleteBurned(muts Mutations, get func(base.OutputID) ([]byte, bool)) (uint64, error) {
	var ret uint64
	for _, oid := range burnedIDs {
		data, found := get(oid)
		if !found {
			continue
		}
		o, err := ledger.OutputFromBytes(data)
		if err != nil {
			return 0, fmt.Errorf("freezeburn: burned output %s: %w", oid.StringShort(), err)
		}
		muts.InsertDelOutputMutation(oid)
		if cc := o.ChainConstraint(); cc != nil {
			chainID := cc.ChainID
			if cc.IsOrigin() {
				chainID = base.MakeOriginChainID(oid)
			}
			muts.InsertDelChainMutation(chainID)
		}
		ret += o.TokenBalance()
	}
	return ret, nil
}
