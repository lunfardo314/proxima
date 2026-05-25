package txbuilder_seq

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
)

type (
	// StemAggregates holds the past-cone-aware aggregates the sequencer task
	// (or the distribute path) plumbs into the produced StemLock. When the
	// caller does not provide them, buildStemLock falls back to a local-input-
	// only auto-compute (sufficient for single-tx past cones — distribute and
	// most simple tests).
	//
	// All values describe the NEW branch being built (i.e. they include this
	// transaction's contribution: its inflation, its consumed inputs, its tx).
	//
	// TotalSupply and TotalCoverage are NOT carried here — the txbuilder always
	// derives them from the predecessor stem using the on-chain recurrence
	// (totalSupply = predSupply + slotInflation; totalCoverage = (predCov >> K)
	// + coverageDelta). This guarantees the produced stem satisfies the
	// stemLock constraint regardless of which off-chain coverage estimator the
	// caller used to compute its own coverage view.
	StemAggregates struct {
		// CoverageDelta / FrozenCoverage / SlotInflation describe the past cone
		// EXCLUDING this branch transaction itself. buildStemLock adds the
		// branch's own chain+branch inflation to SlotInflation and +1 to
		// NumConfirmedTransactions before populating the produced StemLock — this
		// matches what the milestone attacher will compute when validating
		// the branch (which DOES include the branch tx in its past cone).
		CoverageDelta            uint64
		FrozenCoverage           uint64
		SlotInflation            uint64
		NumConfirmedTransactions uint32
		// 24-byte trie root of the predecessor branch (per metadata-refactor §3).
		// Empty / nil leaves Source() emitting 24 zero bytes (genesis convention).
		BaselineRoot []byte
	}

	SeqTxBuilder struct {
		*txbuildercore.TxBuilder
		// Typed mirrors of consumed / produced outputs — bytes live in
		// the embedded core builder; these slices let sequencer code
		// inspect amounts, locks and chain constraints without
		// re-parsing each output.
		ConsumedOutputs []*ledger.Output
		ProducedOutputs []*ledger.Output
		*ledger.Library // cached library for this transaction's slot
		origSeqData     *seqdata.SequencerData
		rdr             multistate.IndexedStateReader
		nextSeqData     *seqdata.SequencerData
		signatureType   byte
		privateKey      []byte
		publicKey       []byte
		chainInput      *ledger.OutputWithChainID
		stemInput       *ledger.OutputWithID // it is branch tx if != nil
		// chainEpochSlots / chainMaxFrozenEpochs are this sequencer chain's
		// own immutable delegation params, carried as the two args of the
		// sequencer constraint at slot 4. Read once in New() and asserted
		// non-zero (every sequencer chain has the constraint, locked at
		// origin).
		chainEpochSlots          uint32
		chainMaxFrozenEpochs     byte
		doNotInflateMainChain    bool    // default is inflate
		chainOutAmounts          []int64 // allocated in New(): AmountIndexFrozenCoverage + chainMaxFrozenEpochs
		vrfProof                 []byte
		branchCoverageUpperBound uint64          // upper bound for branch coverage, 0 means no enforcement
		enforceFreezeUpperBound  bool            // if true, check upper bound before each delegation freeze
		stemAggregates           *StemAggregates // override for buildStemLock; nil → auto-compute
		baselineRoot             []byte          // optional caller-supplied predecessor branch trie root
	}

	TxBuilderCommand interface {
		// Apply valid=false means it is permanently invalid, err is a reason why not possible to apply it
		Apply(txb *SeqTxBuilder) (valid bool, err error)
		Lines(prefix ...string) *lines.Lines
		// AttachmentCostDelta returns the total attachment cost contribution of this command,
		// including the base tag-along input (+1) plus any additional inputs/outputs the command creates.
		// This value is added to seqTxCost to predict the final sequencer transaction cost.
		AttachmentCostDelta() int
	}
	Params struct {
		Timestamp             base.LedgerTime
		Predecessor           *ledger.OutputWithChainID
		Stem                  *ledger.OutputWithID
		SignatureType         byte
		PrivateKey            []byte
		PublicKey             []byte
		StateReader           multistate.IndexedStateReader
		DoNotInflateMainChain bool
	}
)

// New initializes sequencer tx builder and performs necessary validity check
func New(par Params) (*SeqTxBuilder, error) {

	ret := &SeqTxBuilder{
		Library:               ledger.L(par.Timestamp.Slot), // cached library for this transaction's slot
		signatureType:         par.SignatureType,
		privateKey:            par.PrivateKey,
		publicKey:             par.PublicKey,
		chainInput:            par.Predecessor,
		stemInput:             par.Stem,
		TxBuilder:             txbuildercore.New(0),
		ConsumedOutputs:       make([]*ledger.Output, 0),
		ProducedOutputs:       make([]*ledger.Output, 0),
		rdr:                   par.StateReader,
		doNotInflateMainChain: par.DoNotInflateMainChain,
	}
	// Read this sequencer chain's immutable delegation params from the
	// sequencer constraint at slot 4. The constraint is mandatory on
	// every sequencer chain (locked at origin) — its absence is a
	// programming error: SeqTxBuilder must not be invoked on a regular
	// chain.
	seqBytes, err := par.Predecessor.Output.At(int(ledger.SequencerConstraintFixedIndex))
	if err != nil || len(seqBytes) == 0 {
		return nil, fmt.Errorf("SeqTxBuilder: predecessor chain output %s is not a sequencer chain (no sequencer constraint at slot %d)",
			par.Predecessor.ID.StringShort(), ledger.SequencerConstraintFixedIndex)
	}
	seq, err := ledger.SequencerConstraintFromBytesWithLib(seqBytes, ret.Library)
	if err != nil {
		return nil, fmt.Errorf("SeqTxBuilder: invalid sequencer constraint on predecessor chain output: %w", err)
	}
	ret.chainEpochSlots = seq.EpochSlots
	ret.chainMaxFrozenEpochs = seq.MaxFrozenEpochs
	// Sized once: token balance + inflation + per-epoch frozen-coverage
	// slots. chainMaxFrozenEpochs is fixed for a chain's lifetime so a
	// single allocation per builder suffices.
	ret.chainOutAmounts = make([]int64, int(ledger.AmountIndexFrozenCoverage)+int(ret.chainMaxFrozenEpochs))

	sd, err := ledger.ParseSequencerData(par.Predecessor.Output)
	if err != nil {
		ret.origSeqData = seqdata.New()
	} else {
		ret.origSeqData = &sd
	}
	ret.nextSeqData = ret.origSeqData.Clone()
	diffTicksChain := base.DiffTicks(par.Timestamp, par.Predecessor.Timestamp())
	if diffTicksChain < int64(ret.TransactionPaceSequencer) ||
		diffTicksChain < int64(ret.origSeqData.Pace()) {
		return nil, fmt.Errorf("SeqTxBuilder: pace constraint violated: %s", par.Timestamp.String())
	}

	ret.SetTimestamp(par.Timestamp)

	if ret.IsSlotBoundary() {
		if par.Stem == nil {
			return nil, fmt.Errorf("SeqTxBuilder: wrong timestamp or stem for branch transaction: %s", par.Timestamp.String())
		}
	}

	if ret.stemInput != nil {
		// calculate VRF proof for the branch
		prevStem, ok := ret.stemInput.Output.StemLock()
		util.Assertf(ok, "SequencerTxBuilderinconsistency: cannot find previous stem")

		// sign concatenation of predecessor VRFProof with slot number and next VRF proof
		msg := common.Concat(prevStem.VRFProof, base.Slot2Bytes(ret.TxData.Timestamp.Slot))
		ret.vrfProof = common.Concat(base.SignatureTypeED25519, ed25519.Sign(ret.privateKey, msg))
	}

	// form initial amounts vector

	if !ret.doNotInflateMainChain {
		// calculate main chain inflation amount
		if ret.IsSlotBoundary() {
			// from VRF proof for branch
			util.Assertf(len(ret.vrfProof) > 0, "len(vrfProof)>0")
			ret.chainOutAmounts[ledger.AmountIndexInflation] = int64(ret.Library.BranchInflationBonus(ret.vrfProof, par.Timestamp.Slot))
		} else {
			// for non-branch
			if ret.chainInput.Timestamp().Slot != ret.TxData.Timestamp.Slot {
				ret.chainOutAmounts[ledger.AmountIndexInflation] = int64(ret.Library.ChainInflationOneSlot(
					ret.chainInput.Output.TokenBalance()+uint64(ret.chainInput.Output.FrozenCoverage(0)),
					ret.chainInput.Timestamp().Slot,
				))
			}
		}
	}
	predAmounts := par.Predecessor.Output.Amounts()
	ret.chainOutAmounts[ledger.AmountIndexTokenBalance] = int64(predAmounts.TokenBalance()) + ret.chainOutAmounts[ledger.AmountIndexInflation]

	// Frozen coverage at the predecessor adjusted to the epoch of the
	// successor. Uses this chain's own immutable delegation params from
	// the sequencer constraint.
	diffEpochsInt := ret.DiffEpochs(par.Predecessor.ChainID, par.Timestamp, par.Predecessor.Timestamp(), ret.chainEpochSlots)
	util.Assertf(diffEpochsInt >= 0, "diffEpochsInt>=0")
	diffEpochs := uint32(diffEpochsInt)

	maxFrozenEpochs := uint32(ret.chainMaxFrozenEpochs)
	predecessorFrozenCoverageAdjusted := func(i uint32) (result int64) {
		if idx := i + diffEpochs; idx < maxFrozenEpochs {
			result = predAmounts.FrozenCoverageAt(byte(idx))
		}
		return
	}
	for i := uint32(0); i < maxFrozenEpochs; i++ {
		ret.chainOutAmounts[ledger.AmountIndexFrozenCoverage+byte(i)] = predecessorFrozenCoverageAdjusted(i)
	}

	// initialize branch coverage bounds for delegation freeze checking
	if ret.stemInput != nil {
		ret.branchCoverageUpperBound = ret.Library.BranchCoverageUpperBound(par.Timestamp.Slot)
		ret.enforceFreezeUpperBound = !ret.origSeqData.IsIgnoreFreezeBound()
	}

	// consume chain and stem (optionally) outputs but do not unlock it
	idx, err := ret.ConsumeOutput(ret.chainInput.Output, ret.chainInput.ID)
	util.AssertNoError(err)
	util.Assertf(idx == 0, "idx==0")

	if par.Stem != nil {
		idx, err = ret.ConsumeOutput(ret.stemInput.Output, ret.stemInput.ID)
		util.AssertNoError(err)
		util.Assertf(idx == 1, "idx==1")
	}
	return ret, nil
}

func NewWithSequencerID(ts base.LedgerTime,
	seqID base.ChainID,
	privateKey ed25519.PrivateKey,
	rdr multistate.SugaredStateReader) (*SeqTxBuilder, error) {

	seqIn, err := rdr.GetChainOutputWithChainID(seqID)
	if err != nil {
		return nil, fmt.Errorf("error while retrieving chain origin for %s: %w", seqID.String(), err)
	}
	var stemIn *ledger.OutputWithID
	if ts.IsSlotBoundary() {
		stemIn = rdr.GetStemOutput()
	}
	return New(Params{
		Timestamp:     ts,
		Predecessor:   &seqIn,
		Stem:          stemIn,
		SignatureType: base.SignatureTypeED25519,
		PrivateKey:    privateKey,
		PublicKey:     privateKey.Public().(ed25519.PublicKey),
		StateReader:   rdr,
	})
}

// BranchInflationAmount returns the branch inflation bonus this txbuilder
// will apply to its sequencer chain output (set at New time from VRF proof).
// Valid only on branch txs (StemInput != nil); 0 otherwise.
func (txb *SeqTxBuilder) BranchInflationAmount() uint64 {
	if txb.stemInput == nil {
		return 0
	}
	return uint64(txb.chainOutAmounts[ledger.AmountIndexInflation])
}

func (txb *SeqTxBuilder) ChainInput() *ledger.OutputWithChainID {
	return txb.chainInput
}

// ChainDelegationParams returns the (epochSlots, maxFrozenEpochs) values
// inlined into this sequencer chain's sequencer constraint at origin.
// Both are guaranteed non-zero (constraint is mandatory on every
// sequencer chain and bounds-checked in EasyFL: epochSlots ∈ [500,
// 2000], maxFrozenEpochs ∈ [8, 32]).
func (txb *SeqTxBuilder) ChainDelegationParams() (epochSlots uint32, maxFrozenEpochs byte) {
	return txb.chainEpochSlots, txb.chainMaxFrozenEpochs
}

func (txb *SeqTxBuilder) IsSlotBoundary() bool {
	return txb.TxData.Timestamp.IsSlotBoundary()
}

func (txb *SeqTxBuilder) SetInflateMainChain(inflate bool) {
	txb.doNotInflateMainChain = !inflate
}

func (txb *SeqTxBuilder) AddEndorsement(txid base.TransactionID) error {
	txb.TxData.Endorsements = append(txb.TxData.Endorsements, txid)
	if len(txb.TxData.Endorsements) > int(txb.MaxNumberOfEndorsements) {
		return fmt.Errorf("SeqTxBuilder: too many endorsements")
	}
	return nil
}

// AddSimpleInput output must have 2 constraints and lock must be address25519 or chainLock
func (txb *SeqTxBuilder) AddSimpleInput(o ledger.OutputWithID) error {
	idx, err := txb.ConsumeOutput(o.Output, o.ID)
	if err != nil {
		return fmt.Errorf("AddSimpleInput: %v", err)
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(o.Output.TokenBalance())
	switch o.Output.Lock().Name() {
	case ledger.SigLockName:
		if err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0); err != nil {
			return fmt.Errorf("AddSimpleInput: %v", err)
		}
	case ledger.ChainLockName:
		txb.PutUnlockParams(idx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))
	default:
		return fmt.Errorf("AddSimpleInput: wrong ock type")
	}
	return nil
}

// AddTagAlongInput returns:
//
//	-- false, error if output is permanently invalid. If err != nil, it is a reason why
//	-- true, error it is temporary cannot be applied
func (txb *SeqTxBuilder) AddTagAlongInput(o ledger.OutputWithID) (cmd TxBuilderCommand, valid bool, err error) {
	if cmd, valid, err = txb.TxBuilderCommandFromOutput(o); err == nil {
		valid, err = cmd.Apply(txb)
	}
	if err != nil {
		err = fmt.Errorf("AddTagAlongInput: %w", err)
	}
	return
}

func (txb *SeqTxBuilder) calcAdvance(delegationIn *ledger.DelegationOutput, frozenEpochs byte) (uint64, error) {
	delegatorRequirement := delegationIn.RequiredInflationShare
	seqTolerance := 1000 - txb.origSeqData.InflationProfitMarginPromille()
	if seqTolerance < delegatorRequirement {
		return 0, fmt.Errorf("SeqTxBuilder.FreezeDelegation: advance required by delegator is loss-making for the sequencer")
	}
	// delegationIn carries inlined epochSlots; use it directly.
	frozenSlots := txb.FrozenSlotsFromFrozenEpochs(delegationIn.Target, txb.TxData.Timestamp.Slot, delegationIn.EpochSlots, frozenEpochs)
	projectedInflation := txb.Library.ChainInflationMultiStep(delegationIn.Output.TokenBalance(), txb.TxData.Timestamp.Slot, frozenSlots)

	if txb.origSeqData.IsGreedy() {
		return (projectedInflation * uint64(delegatorRequirement)) / 1000, nil
	}
	return (projectedInflation * uint64(seqTolerance)) / 1000, nil
}

// FreezeDelegation makes delegated output frozen. Returned valid = false if output is permanently invalid and freezing should not be repeated again
func (txb *SeqTxBuilder) FreezeDelegation(delegationIn *ledger.DelegationOutput, freezeUntilEpoch ...uint32) (successorIdx byte, valid bool, err error) {
	if !delegationIn.IsUnlockableByTargetForFreezing(txb.TxData.Timestamp.Slot) {
		valid = true
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: output cannot be unlocked by the target for freezing:\n%s", delegationIn.LinesHRFull("   ").String())
		return
	}
	if len(txb.ConsumedOutputs) > 255 {
		valid = true
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: too many inputs")
		return
	}
	if len(txb.ProducedOutputs) > 254 {
		valid = true
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: too many produced outputs")
		return
	}
	if delegationIn.Target != txb.chainInput.ChainID {
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: cannot be unlocked by the sequencer at %s", txb.TxData.Timestamp.String())
		return
	}
	txEpoch := txb.EpochFromSlotDirect(delegationIn.Target, txb.TxData.Timestamp.Slot, delegationIn.EpochSlots)

	freezeMaxEpoch := delegationIn.FreezeUntilMax(txb.TxData.Timestamp)
	var lastEpochToFreeze uint32
	if len(freezeUntilEpoch) > 0 && freezeUntilEpoch[0] <= freezeMaxEpoch && freezeUntilEpoch[0] >= txEpoch {
		lastEpochToFreeze = freezeUntilEpoch[0]
	} else {
		lastEpochToFreeze = freezeMaxEpoch
	}
	util.Assertf(lastEpochToFreeze >= txEpoch, "lastEpochToFreeze>=txEpoch")

	frozenEpochs := lastEpochToFreeze - txEpoch + 1
	var advance uint64
	if advance, err = txb.calcAdvance(delegationIn, byte(frozenEpochs)); err != nil {
		return
	}
	predIdx := byte(len(txb.ConsumedOutputs))
	delegationOut, err := delegationIn.MakeDelegationFreezeOutput(
		txb.TxData.Timestamp, lastEpochToFreeze, predIdx, advance)
	if err != nil {
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: %w", err)
		return
	}

	// check if sequencer has enough token balance to pay the advance
	// If not, consider delegation to be permanently invalid (even not true 100%)
	if txb.chainOutAmounts[ledger.AmountIndexTokenBalance] < int64(advance) {
		valid = false
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: not enough token balance for advance (%s < %s)",
			util.Th(uint64(txb.chainOutAmounts[ledger.AmountIndexTokenBalance])), util.Th(advance))
		return
	}

	// check if freezing this delegation would push coverage above the upper bound
	if txb.enforceFreezeUpperBound {
		projectedTokenBalance := txb.chainOutAmounts[ledger.AmountIndexTokenBalance] - int64(advance)
		projectedFrozen0 := txb.chainOutAmounts[ledger.AmountIndexFrozenCoverage] +
			delegationOut.Amounts().FrozenCoverageAt(0)
		projectedCoverage := uint64(projectedTokenBalance + projectedFrozen0)
		if projectedCoverage > txb.branchCoverageUpperBound {
			valid = true
			err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: skipping, would exceed branch coverage upper bound (%s > %s)",
				util.Th(projectedCoverage), util.Th(txb.branchCoverageUpperBound))
			return
		}
	}

	idx, err := txb.ConsumeOutput(delegationIn.Output, delegationIn.ID)
	if err != nil {
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: %w", err)
		return
	}
	util.Assertf(idx == predIdx, "idx == predIdx")

	successorIdx, err = txb.ProduceOutput(delegationOut)
	if err != nil {
		err = fmt.Errorf("SeqTxBuilder.FreezeDelegation: %w", err)
		return
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] -= int64(advance)
	// unlock delegation lock as target. First byte is chain lock unlock, 2nd byte indicates it is target unlock
	txb.PutUnlockParams(idx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), ledger.DelegationUnlockedByTarget)
	// unlock chain
	txb.PutUnlockParams(idx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(successorIdx))

	// add frozen coverage to the sequencer output. Vector size is this
	// chain's chainMaxFrozenEpochs — the delegation targets this chain
	// so its TargetMaxFrozenEpochs equals our chainMaxFrozenEpochs.
	a := delegationOut.Amounts().FrozenCoverageVector(txb.chainMaxFrozenEpochs)
	for i, c := range a {
		txb.chainOutAmounts[ledger.AmountIndexFrozenCoverage+byte(i)] += c
	}
	valid = true
	return
}

func (txb *SeqTxBuilder) AddWithdrawOutput(o *ledger.Output) error {
	// Withdrawal output must carry no inflation and no frozen coverage.
	// IsFrozenCoverageZero(N) reads positions [2 .. 2+N-1]; reading past
	// NumElements returns 0 from Amount(), so passing the chain's
	// chainMaxFrozenEpochs covers every position that could legitimately
	// hold a non-zero FC cell on this chain.
	if o.Inflation() != 0 || !o.Amounts().IsFrozenCoverageZero(txb.chainMaxFrozenEpochs) {
		return fmt.Errorf("AddWithdrawOutput: only token balance can be non-zero")
	}
	amount := o.TokenBalance()
	if txb.chainOutAmounts[ledger.AmountIndexTokenBalance] < int64(amount) {
		return fmt.Errorf("AddWithdrawOutput: not enough token balance")
	}
	if _, err := txb.ProduceOutput(o); err != nil {
		return fmt.Errorf("AddWithdrawOutput: %w", err)
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] -= int64(o.TokenBalance())
	return nil
}

func (txb *SeqTxBuilder) buildSequencerAndStemOutputs() error {
	// sequencer input
	txb.PutSignatureUnlock(0)

	// sequencer produced output
	chainOutIdx, err := txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.PutAmounts(txb.chainOutAmounts[:]...)
		o.PutLock(txb.chainInput.Output.Lock())

		// chain constraint at fixed index 2
		// compute cumulative inflation values for the chain constraint
		totalInflation := uint64(txb.chainOutAmounts[ledger.AmountIndexInflation])
		var chainInflation, branchBonus uint64
		if txb.stemInput != nil {
			// branch transaction: all inflation is branch bonus
			branchBonus = totalInflation
		} else {
			// non-branch transaction: all inflation is chain inflation
			chainInflation = totalInflation
		}
		var branchCounterInc uint32
		if txb.stemInput != nil {
			branchCounterInc = 1
		}
		chainOutConstraint := ledger.NewChainConstraint(
			txb.chainInput.ChainID, 0, txb.chainInput.OriginSlot,
			txb.chainInput.CumulativeChainInflation+chainInflation,
			txb.chainInput.CumulativeBranchBonus+branchBonus,
			txb.chainInput.TransitionCounter+1,
			txb.chainInput.BranchCounter+branchCounterInc,
		)
		o.PutConstraint(chainOutConstraint.Bytes(), ledger.ConstraintIndexChain)
		// Sequencer constraint carries the immutable delegation params
		// (epochSlots, maxFrozenEpochs). Args byte-equal across every
		// transit (selfImmutableOnSuccessorIndex on the constraint
		// itself), so we re-emit what the predecessor carried.
		sequencerConstraint := ledger.NewSequencerConstraint(txb.chainEpochSlots, txb.chainMaxFrozenEpochs)
		idxSeq := o.MustPushConstraint(sequencerConstraint.Bytes())
		util.Assertf(idxSeq == ledger.SequencerConstraintFixedIndex, "idxSeq == SequencerConstraintFixedIndex")
		idxMsData := o.MustPushConstraint(easyfl.InlineDataBytecode(txb.nextSeqData.Bytes()))
		util.Assertf(idxMsData == ledger.SeqMilestoneDataFixedIndex, "idxMsData == SeqMilestoneDataFixedIndex")
	}))
	if err != nil {
		return fmt.Errorf("SeqTxBuilder: %w", err)
	}

	// unlock sequencer chain constraint
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(chainOutIdx))

	if txb.stemInput == nil {
		// Non-branch: stem index stays at the SequencerOutputIndexNone sentinel (0xff).
		txb.SetSequencerData(chainOutIdx, txbuildercore.SequencerOutputIndexNone)
		return nil
	}
	// handle stem
	stemLock := txb.buildStemLock()
	stemOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(txb.stemInput.Output.TokenBalance()))
		o.WithLock(stemLock)
	})
	stemIdx, err := txb.ProduceOutput(stemOut)
	if err != nil {
		return fmt.Errorf("SeqTxBuilder: %w", err)
	}
	txb.SetSequencerData(chainOutIdx, stemIdx)
	return nil
}

// SetStemAggregates plumbs past-cone-aware values into the stem build path.
// When set, buildStemLock uses these values verbatim and asserts the supply/
// coverage recurrences against the predecessor stem (B3 sanity check). When
// unset, buildStemLock falls back to a local-input-only auto-compute that is
// correct for single-tx past cones (distribute / simple tests).
func (txb *SeqTxBuilder) SetStemAggregates(a StemAggregates) {
	txb.stemAggregates = &a
}

// buildStemLock assembles the new branch's StemLock. The on-chain recurrence
// drives TotalSupply / TotalCoverage in both paths so the produced stem always
// satisfies the stemLock constraint. The remaining aggregates come either from
// the caller (SetStemAggregates — past-cone-aware) or are auto-computed from
// the txbuilder's local view (single-tx past cone — distribute / simple tests).
func (txb *SeqTxBuilder) buildStemLock() *ledger.StemLock {
	prevStem, ok := txb.stemInput.Output.StemLock()
	util.Assertf(ok, "buildStemLock: stem input is not a stem output")

	// K = txSlot - predBranchSlot. Used by the totalCoverage halving recurrence.
	predTxID := txb.stemInput.ID.TransactionID()
	predBranchSlot := predTxID.Timestamp().Slot
	curSlot := txb.TxData.Timestamp.Slot
	util.Assertf(curSlot >= predBranchSlot, "buildStemLock: curSlot(%d) < predBranchSlot(%d)", curSlot, predBranchSlot)
	k := uint64(curSlot - predBranchSlot)

	var coverageDelta, frozenCoverage, slotInflation uint64
	var numConfirmedTransactions uint32
	var baselineRoot []byte

	if a := txb.stemAggregates; a != nil {
		// Past-cone aggregates from the caller — fold in this branch tx's
		// own chain+branch inflation and +1 transaction so the produced stem
		// matches what the milestone attacher will later compute.
		coverageDelta = a.CoverageDelta
		frozenCoverage = a.FrozenCoverage
		slotInflation = a.SlotInflation + uint64(txb.chainOutAmounts[ledger.AmountIndexInflation])
		numConfirmedTransactions = a.NumConfirmedTransactions + 1
		baselineRoot = a.BaselineRoot
	} else {
		// Auto-compute fallback (single-tx past cone): coverageDelta / frozen /
		// slotInflation come from THIS tx's inputs and inflation; numConfirmedTransactions = 1.
		for _, o := range txb.ConsumedOutputs {
			coverageDelta += o.TokenBalance()
			frozenCoverage += uint64(o.FrozenCoverage(0))
		}
		slotInflation = uint64(txb.chainOutAmounts[ledger.AmountIndexInflation])
		numConfirmedTransactions = 1
	}
	// Fall back to deriving baselineRoot if the caller didn't supply it.
	// Prefer the explicit txb.baselineRoot setter (used by the distribute
	// path); otherwise read from the state reader the txbuilder is built on.
	if len(baselineRoot) == 0 {
		switch {
		case len(txb.baselineRoot) > 0:
			baselineRoot = txb.baselineRoot
		case txb.rdr != nil:
			if root := txb.rdr.Root(); root != nil {
				baselineRoot = root.Bytes()
			}
		}
	}

	// Trustless-stats sanity (Phase B3): frozen must be strictly less than
	// coverageDelta. In auto-compute paths we treat equality as a defensive
	// reset to 0; in the override path equality means the caller miscounted —
	// surface it loudly.
	if txb.stemAggregates == nil && frozenCoverage >= coverageDelta {
		frozenCoverage = 0
	}
	util.Assertf(frozenCoverage < coverageDelta || coverageDelta == 0,
		"buildStemLock: FrozenCoverage(%d) must be strictly less than CoverageDelta(%d)",
		frozenCoverage, coverageDelta)

	// On-chain recurrence — same formula the stemLock constraint enforces.
	totalSupply := prevStem.TotalSupply + slotInflation
	predTotalCov := prevStem.TotalCoverage
	if k >= 64 {
		predTotalCov = 0
	} else {
		predTotalCov >>= k
	}
	totalCoverage := predTotalCov + coverageDelta

	return &ledger.StemLock{
		PredecessorOutputID:      txb.stemInput.ID,
		VRFProof:                 txb.vrfProof,
		TotalSupply:              totalSupply,
		TotalCoverage:            totalCoverage,
		CoverageDelta:            coverageDelta,
		FrozenCoverage:           frozenCoverage,
		SlotInflation:            slotInflation,
		NumConfirmedTransactions: numConfirmedTransactions,
		BaselineRoot:             baselineRoot,
	}
}

// BytesWithInputLoader finalises the sequencer transaction (sequencer
// and stem outputs, input commitment, signature) and returns its raw
// bytes together with a bytes-loader suitable for
// transaction.ParseAndValidate.
func (txb *SeqTxBuilder) BytesWithInputLoader() ([]byte, func(i byte) ([]byte, error), error) {
	if err := txb.buildSequencerAndStemOutputs(); err != nil {
		return nil, nil, fmt.Errorf("SeqTxBuilder: %w", err)
	}
	txb.ComputeInputCommitment()
	txb.SignED25519(txb.privateKey)

	return txb.TxBuilder.Bytes(), txb.LoadInputBytes, nil
}

func (txb *SeqTxBuilder) reservedInputs() (ret int) {
	ret = 1
	if txb.stemInput != nil {
		ret = 2
	}
	return
}

func (txb *SeqTxBuilder) StateReader() multistate.SugaredStateReader {
	return multistate.MakeSugared(txb.rdr)
}

func (txb *SeqTxBuilder) InputsAreFull() bool {
	return txb.NumInputs()+txb.reservedInputs() >= 256
}

// AttachmentCost returns the predicted final attachment cost of the sequencer transaction.
// This is the sum of inputs and outputs, including chain output and stem output (if branch)
// that will be added at finalization.
func (txb *SeqTxBuilder) AttachmentCost() int {
	// Current inputs + current outputs + chain output (always 1)
	cost := txb.NumInputs() + txb.NumOutputs() + 1
	if txb.stemInput != nil {
		// Stem output will be added for branch transactions
		cost++
	}
	return cost
}

func (txb *SeqTxBuilder) Timestamp() base.LedgerTime {
	return txb.TxData.Timestamp
}

func (txb *SeqTxBuilder) Slot() uint32 {
	return txb.TxData.Timestamp.Slot
}

// CurrentBranchCoverage returns tokenBalance + frozenCoverage[epoch 0] of the sequencer chain output being built.
func (txb *SeqTxBuilder) CurrentBranchCoverage() uint64 {
	return uint64(txb.chainOutAmounts[ledger.AmountIndexTokenBalance] +
		txb.chainOutAmounts[ledger.AmountIndexFrozenCoverage])
}

// EffectiveName returns the current name from nextSeqData (inherited from predecessor).
func (txb *SeqTxBuilder) EffectiveName() string {
	return txb.nextSeqData.Name()
}

func (txb *SeqTxBuilder) SetName(name string) {
	txb.nextSeqData.SetName(name)
}

type MakeSimpleSequencerTransactionParams struct {
	// sequencer name (set only if != ""
	SeqName string
	// transaction ts
	Timestamp base.LedgerTime
	// predecessor
	ChainInput *ledger.OutputWithChainID
	//
	StemInput *ledger.OutputWithID // it is branch tx if != nil
	// timestamp of the transaction
	// additional inputs to consume. Must be unlockable by chain
	// can contain sender commands to the sequencer
	AdditionalInputs []*ledger.OutputWithID
	// withdraw outputs
	WithdrawOutputs []*ledger.Output
	// Endorsements
	Endorsements []base.TransactionID
	// ExplicitBaseline or nil if none
	ExplicitBaseline *base.TransactionID
	// private key type
	SignatureType byte
	// chain controller
	PrivateKey []byte
	//
	PublicKey []byte
	//
	DoNotInflateMainChain bool
	//
	AttachmentBudget uint16
	// BaselineRoot is the predecessor branch's trie root (24 bytes). Required
	// for branch txs (StemInput != nil) so the produced stem's BaselineRoot
	// matches what the attacher cross-checks (metadata-refactor §9.4).
	BaselineRoot []byte
	// StemAggregates, if non-nil, overrides buildStemLock's local-input-only
	// auto-compute with past-cone-aware values. Set this in tests that build
	// a branch transaction over a multi-tx past cone — the strict attacher
	// check rejects single-tx auto-compute aggregates when the actual past
	// cone has more than one new transaction (metadata-refactor §9.6).
	StemAggregates *StemAggregates
}

// MakeSimpleSequencerTransactionWithInputLoader usually used in tests
func MakeSimpleSequencerTransactionWithInputLoader(par MakeSimpleSequencerTransactionParams) ([]byte, func(i byte) ([]byte, error), error) {
	if !ledger.ValidSequencerPace(par.ChainInput.Timestamp(), par.Timestamp) {
		return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: sequencer pace constraint violated with chain input")
	}
	if par.StemInput != nil {
		if !ledger.ValidSequencerPace(par.StemInput.Timestamp(), par.Timestamp) {
			return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: sequencer pace constraint violated with stem input")
		}
	}
	for _, o := range par.AdditionalInputs {
		if !ledger.ValidSequencerPace(o.Timestamp(), par.Timestamp) {
			return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: sequencer pace constraint violated with additional input")
		}
	}
	txb, err := New(Params{
		Timestamp:             par.Timestamp,
		Predecessor:           par.ChainInput,
		Stem:                  par.StemInput,
		SignatureType:         par.SignatureType,
		PrivateKey:            par.PrivateKey,
		PublicKey:             par.PublicKey,
		DoNotInflateMainChain: par.DoNotInflateMainChain,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: %w", err)
	}
	if par.SeqName != "" {
		txb.SetName(par.SeqName)
	}
	if len(par.BaselineRoot) > 0 {
		// Caller-supplied predecessor branch trie root — used by buildStemLock
		// to populate the produced stem's BaselineRoot (metadata-refactor §9.4).
		txb.baselineRoot = par.BaselineRoot
	}
	if par.StemAggregates != nil {
		txb.SetStemAggregates(*par.StemAggregates)
	}
	for _, endorsement := range par.Endorsements {
		if err = txb.AddEndorsement(endorsement); err != nil {
			return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: %w", err)
		}
	}
	if par.ExplicitBaseline != nil {
		if !par.ExplicitBaseline.IsBranchTransaction() {
			return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: explicit baseline must be a branch transaction ID, got %s", par.ExplicitBaseline.StringShort())
		}
		txb.PutExplicitBaseline(par.ExplicitBaseline)
	}
	for _, o := range par.AdditionalInputs {
		if err = txb.AddSimpleInput(*o); err != nil {
			return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: %w", err)
		}
	}
	for _, o := range par.WithdrawOutputs {
		if err = txb.AddWithdrawOutput(o); err != nil {
			return nil, nil, fmt.Errorf("MakeSequencerTransactionWithInputLoader: %w", err)
		}
	}
	return txb.BytesWithInputLoader()
}

func MakeSimpleSequencerTransaction(par MakeSimpleSequencerTransactionParams) ([]byte, error) {
	txBytes, _, err := MakeSimpleSequencerTransactionWithInputLoader(par)
	return txBytes, err
}
