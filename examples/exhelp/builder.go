// Package exhelp is a typed-output convenience layer over
// txbuildercore.TxBuilder, shared by Proxima's in-tree examples
// (chess_poc, dex) and the proxi chess_cmd that drives them.
//
// txbuildercore is wasm-clean and bytes-only by design; examples want
// to compose with *ledger.Output ergonomics (consume/produce typed
// outputs, sum amounts, parse them back after the fact). This wrapper
// holds the typed mirrors and exposes the few extra methods example
// callers need, without polluting txbuildercore with ledger-typed APIs.
package exhelp

import (
	"crypto/ed25519"
	"fmt"
	"math"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
)

// Builder embeds *txbuildercore.TxBuilder and mirrors consumed /
// produced outputs as *ledger.Output for typed inspection.
type Builder struct {
	*txbuildercore.TxBuilder
	ConsumedOutputs []*ledger.Output
	ProducedOutputs []*ledger.Output
}

// New returns a fresh Builder with the upgrade-index slot left at 0;
// it is populated by SetTimestamp from the ledger library at the
// chosen slot.
func New() *Builder {
	return &Builder{
		TxBuilder:       txbuildercore.New(0),
		ConsumedOutputs: make([]*ledger.Output, 0),
		ProducedOutputs: make([]*ledger.Output, 0),
	}
}

// SetTimestamp sets the tx timestamp and the UpgradeIndex from the
// library version at that slot. Examples use the in-process ledger
// singleton.
func (b *Builder) SetTimestamp(ts base.LedgerTime) {
	b.TxBuilder.SetTimestamp(ts)
	b.TxData.UpgradeIndex = ledger.L(ts.Slot).UpgradeIndex()
}

// ConsumeOutput appends a typed consumed output and forwards its raw
// bytes to the embedded core builder.
func (b *Builder) ConsumeOutput(out *ledger.Output, oid base.OutputID) (byte, error) {
	if b.NumInputs() >= 256 {
		return 0, fmt.Errorf("too many consumed outputs")
	}
	b.ConsumedOutputs = append(b.ConsumedOutputs, out)
	return b.TxBuilder.ConsumeOutput(out.Bytes(), oid), nil
}

// ConsumeOutputsUnlock consumes a sequence of sigLock outputs, signing
// the first input and unlock-referencing the rest. Returns total
// balance and the maximum timestamp across the consumed outputs.
func (b *Builder) ConsumeOutputsUnlock(outs ...*ledger.OutputWithID) (uint64, base.LedgerTime, error) {
	if len(outs) >= 256 {
		return 0, base.LedgerTime{}, fmt.Errorf("ConsumeOutputsUnlock: number of inputs can't be greater than 256")
	}
	total := uint64(0)
	maxTs := base.LedgerTime{}
	for i, o := range outs {
		if o.Output.Lock().Name() != ledger.SigLockName {
			return 0, base.LedgerTime{}, fmt.Errorf("ConsumeOutputsUnlock: only SigLock locks are allowed")
		}
		if o.Output.TokenBalance() >= math.MaxUint64-total {
			return 0, base.LedgerTime{}, fmt.Errorf("ConsumeOutputsUnlock: amount overflow")
		}
		if _, err := b.ConsumeOutput(o.Output, o.ID); err != nil {
			return 0, base.LedgerTime{}, err
		}
		if i == 0 {
			b.PutSignatureUnlock(0)
		} else {
			if err := b.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0); err != nil {
				return 0, base.LedgerTime{}, err
			}
		}
		total += o.Output.TokenBalance()
		maxTs = base.MaximumTime(maxTs, o.Timestamp())
	}
	return total, maxTs, nil
}

// ReplaceProducedOutput overwrites the produced output at idx, syncing
// both the typed buffer and the wire-format byte slice. Used by
// callers that mutate a produced output after the initial Push (e.g.
// frozen-coverage post-processing on a chain output).
func (b *Builder) ReplaceProducedOutput(idx byte, o *ledger.Output) {
	b.ProducedOutputs[idx] = o
	b.TxData.OutputBytes[idx] = o.Bytes()
}

// CalcFrozenCoverageDelta sums up frozen-coverage vectors of all
// produced delegation outputs in this tx. The result is sized at the
// max NumElements observed across them (effectively the chain's
// maxFrozenEpochs since all delegations in a freeze tx target the
// same chain). Returns nil if no delegation output has any FC cells.
func (b *Builder) CalcFrozenCoverageDelta() ([]int64, error) {
	maxLen := 0
	for _, o := range b.ProducedOutputs {
		if o.Lock().Name() != ledger.DelegateLockName {
			continue
		}
		n := o.Amounts().NumElements()
		if n > maxLen {
			maxLen = n
		}
	}
	if maxLen < int(ledger.AmountIndexFrozenCoverage) {
		return nil, nil
	}
	sum := make([]int64, maxLen)
	for _, o := range b.ProducedOutputs {
		if o.Lock().Name() == ledger.DelegateLockName {
			if overflow := o.Amounts().AddToVector(sum); overflow {
				return nil, fmt.Errorf("CalcFrozenCoverageDelta: arithmetic overflow")
			}
		}
	}
	return sum[ledger.AmountIndexFrozenCoverage:], nil
}

// MustPutFrozenCoverage adjusts the produced chain output's amounts
// vector to carry forward the predecessor's frozen coverage (shifted
// by the inter-tx epoch difference) plus the per-epoch deltas from
// produced delegation outputs. epochSlots and maxFrozenEpochs are
// read from the produced output's sequencer constraint at
// SequencerConstraintFixedIndex.
func (b *Builder) MustPutFrozenCoverage(producedOutputIdx byte, frozenCoverageDeltaVector []int64, targetTs base.LedgerTime) {
	o := b.ProducedOutputs[producedOutputIdx]

	lib := ledger.L(targetTs.Slot)

	seqBytes, seqErr := o.At(int(ledger.SequencerConstraintFixedIndex))
	util.Assertf(seqErr == nil && len(seqBytes) > 0,
		"MustPutFrozenCoverage: produced chain output must be a sequencer chain (carry the sequencer constraint) to receive frozen coverage")
	seq, err := ledger.SequencerConstraintFromBytesWithLib(seqBytes, lib)
	util.AssertNoError(err)

	a := make([]int64, int(ledger.AmountIndexFrozenCoverage)+int(seq.MaxFrozenEpochs))
	a[ledger.AmountIndexTokenBalance] = int64(o.TokenBalance())
	a[ledger.AmountIndexInflation] = int64(o.Inflation())
	copy(a[ledger.AmountIndexFrozenCoverage:], frozenCoverageDeltaVector)

	cc := o.ChainConstraint()
	util.Assertf(cc != nil, "MustPutFrozenCoverage: inconsistency 1")
	oPred := b.ConsumedOutputs[cc.PredecessorInputIndex]
	predVector := oPred.Amounts().FrozenCoverageVector(seq.MaxFrozenEpochs)
	predTs := b.TxData.InputIDs[cc.PredecessorInputIndex].Timestamp()
	predVectorAdjusted := lib.AdjustFrozenCoverageVector(cc.ChainID, predVector, predTs, targetTs,
		seq.EpochSlots, seq.MaxFrozenEpochs)
	for i := range frozenCoverageDeltaVector {
		a[int(ledger.AmountIndexFrozenCoverage)+i] += predVectorAdjusted[i]
	}

	// The sequencer constraint now carries a per-milestone coverageDelta that must
	// strictly increase over the predecessor's within a slot (def/sequencer.easyfl
	// _enforceCoverageAdvance). These single-tx test transits don't track real
	// coverage, so synthesize a strictly-advancing value (predecessor + 1) to keep
	// the constraint satisfied. The exact value is not consensus-checked here
	// (utxodb settlement runs only the EasyFL constraints, not the milestone
	// attacher's computed-vs-declared equality).
	var predCoverageDelta uint64
	if predSeq, idx := oPred.SequencerConstraint(); idx != 0xff {
		predCoverageDelta = predSeq.CoverageDelta
	}
	advancedSeq := ledger.NewSequencerConstraint(seq.EpochSlots, seq.MaxFrozenEpochs, predCoverageDelta+1)

	b.ReplaceProducedOutput(producedOutputIdx, o.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(ledger.NewAmounts(a[:]...).Bytes(), ledger.ConstraintIndexAmounts)
		o.PutConstraint(advancedSeq.Bytes(), ledger.SequencerConstraintFixedIndex)
	}))
}

// ConsumeOutputsNoUnlock consumes a sequence of outputs without
// writing any unlock data — callers wire unlocks themselves (used when
// inputs need custom unlock patterns: chain unlocks, redeem-script
// outputs, foundry transits, …).
func (b *Builder) ConsumeOutputsNoUnlock(outs ...*ledger.OutputWithID) (uint64, base.LedgerTime, error) {
	total := uint64(0)
	maxTs := base.NilLedgerTime
	for _, o := range outs {
		if _, err := b.ConsumeOutput(o.Output, o.ID); err != nil {
			return 0, base.NilLedgerTime, err
		}
		if o.Output.TokenBalance() > math.MaxUint64-total {
			return 0, base.NilLedgerTime, fmt.Errorf("ConsumeOutputsNoUnlock: arithmetic overflow")
		}
		total += o.Output.TokenBalance()
		maxTs = base.MaximumTime(maxTs, o.Timestamp())
	}
	return total, maxTs, nil
}

// ProduceOutput adds a produced output (typed) and forwards its bytes
// to the embedded core builder. Enforces storage-deposit minimum.
func (b *Builder) ProduceOutput(o *ledger.Output) (byte, error) {
	if err := o.EnoughAmountForStorageDeposit(); err != nil {
		return 0, fmt.Errorf("exhelp.ProduceOutput: %v", err)
	}
	o.MustValidOutput()
	if b.NumOutputs() >= 256 {
		return 0, fmt.Errorf("too many produced outputs")
	}
	b.ProducedOutputs = append(b.ProducedOutputs, o)
	return b.TxBuilder.ProduceOutput(o.Bytes()), nil
}

// ConsumedAmount sums token balances across all typed consumed outputs.
func (b *Builder) ConsumedAmount() uint64 {
	ret := uint64(0)
	for _, o := range b.ConsumedOutputs {
		ret += o.TokenBalance()
	}
	return ret
}

// ProducedAmount returns (total balance, total inflation) across the
// typed produced outputs.
func (b *Builder) ProducedAmount() (uint64, uint64) {
	total := uint64(0)
	inflation := uint64(0)
	for _, o := range b.ProducedOutputs {
		total += o.TokenBalance()
		inflation += o.Inflation()
	}
	return total, inflation
}

// LoadInputBytes returns the raw bytes of the i-th consumed output —
// the loader shape expected by transaction.ParseAndValidate.
func (b *Builder) LoadInputBytes(i byte) ([]byte, error) {
	if int(i) >= len(b.ConsumedOutputs) {
		return nil, fmt.Errorf("can't load input #%d", i)
	}
	return b.ConsumedOutputs[i].Bytes(), nil
}

// DeclareTokenConservation pushes a pure-conservation token sentinel
// constraint onto TxConstraints — declares the tag for Phase D
// auditability and asserts Σ consumed(tag) == Σ produced(tag) with no
// foundry transit.
func (b *Builder) DeclareTokenConservation(tag base.ChainID) {
	b.PushTxConstraint(ledger.TokenSentinelBytecode(tag))
}

// FinaliseAndSign sets the timestamp, computes the input commitment
// and signs the transaction with the supplied ed25519 key.
func (b *Builder) FinaliseAndSign(ts base.LedgerTime, priv ed25519.PrivateKey) {
	b.SetTimestamp(ts)
	b.ComputeInputCommitment()
	b.SignED25519(priv)
}

// TransitFoundry consumes a foundry chain output and produces a
// transited foundry output with the supply updated to newSupply. Wires
// chain unlock between input and output and pushes a
// `token(chainID, foundryProducedIdx)` constraint onto TxConstraints.
// The foundry constraint itself self-locks at foundryConstraintIndex
// (slot 4) across every transit — the successor MUST also carry a
// foundry constraint at that slot, only the supply arg may differ.
// The optional foundry policy at slot 5 is self-locked separately by
// the policy body (typically via selfImmutableOnSuccessorIndex).
func (b *Builder) TransitFoundry(inChainData *ledger.OutputDataWithChainID, newSupply uint64) (byte, error) {
	chainIN, err := ledger.OutputFromBytesWithLib(inChainData.Data, ledger.L(inChainData.ID.Slot()))
	if err != nil {
		return 0, fmt.Errorf("TransitFoundry: parse input: %w", err)
	}
	cc := chainIN.ChainConstraint()
	if cc == nil {
		return 0, fmt.Errorf("TransitFoundry: input is not a chained output")
	}
	if _, err := chainIN.ConstraintAt(ledger.ConstraintIndexFoundry); err != nil {
		return 0, fmt.Errorf("TransitFoundry: input has no foundry constraint at slot %d: %w", ledger.ConstraintIndexFoundry, err)
	}
	predIdx, err := b.ConsumeOutput(chainIN, inChainData.ID)
	if err != nil {
		return 0, err
	}
	successor := ledger.NewChainConstraint(
		inChainData.ChainID, predIdx, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
		cc.TransitionCounter+1, cc.BranchCounter,
	)
	newFoundry := ledger.NewFoundry(newSupply)
	chainOut := chainIN.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(successor.Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(newFoundry.Bytes(), ledger.ConstraintIndexFoundry)
	})
	producedIdx, err := b.ProduceOutput(chainOut)
	if err != nil {
		return 0, err
	}
	b.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(producedIdx))
	b.PushTxConstraint(ledger.TokenFoundryBytecode(inChainData.ChainID, producedIdx))
	return producedIdx, nil
}

// MakeFoundryOriginOutput builds a foundry chain-origin output:
// amounts + lock at index 2, chain origin at index 3, foundry(supply)
// at index 4, and optional policy bytecode at index 5. initialSupply
// is typically 0 at origin (no real tag exists yet — minting happens
// at a later transit). The foundry constraint pins itself at slot 4
// across every transit (cannot be dropped or moved); any policy at
// slot 5 self-locks via selfImmutableOnSuccessorIndex.
func MakeFoundryOriginOutput(amount uint64, lock ledger.Lock, originSlot uint32, initialSupply uint64, policyScript []byte) *ledger.Output {
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(amount)).WithLock(lock)
		o.PutConstraint(ledger.NewChainOrigin(originSlot).Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(ledger.NewFoundry(initialSupply).Bytes(), ledger.ConstraintIndexFoundry)
		if len(policyScript) > 0 {
			o.PutConstraint(policyScript, ledger.ConstraintIndexFoundryPolicy)
		}
	})
}
