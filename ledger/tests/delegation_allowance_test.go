package tests

import (
	"fmt"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/util/smallkv"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ed25519"
)

// Tests for the delegation allowance: an ensureStopDelegation argument that
// lets the target sequencer charge the askstop compensation to the
// delegation balance instead of to the delegator's own tokens. The
// delegateLock's non-decrease gate is relaxed by exactly that amount when a
// third unlock byte points at the consumed request output.

const allowanceRequestFee = 1_000

// makeAllowanceRequestOutput produces, in its own transaction signed by
// `signer`, the tag-along command output carrying ensureStopDelegation at
// element 4. It has to be a separate transaction because tagAlong pins its
// senderID to the signer of the transaction that produced it — that binding
// is what makes the allowance an authorisation rather than a self-asserted
// field, so the tests must go through it rather than hand-building the
// output inside the revoke transaction.
func (td *testData) makeAllowanceRequestOutput(
	ts base.LedgerTime,
	signer ed25519.PrivateKey,
	sender ledger.SigLock,
	namedDelegation base.ChainID,
	allowance uint64,
) *ledger.OutputWithID {
	par, err := td.u.MakeTransferInputData(signer, nil, ts)
	require.NoError(td, err)

	reqParams := smallkv.New()
	reqParams.Set(byte(0), []byte{3}) // FieldCmdCode = RequestCodeAskStopDelegation
	reqParams.Set('i', namedDelegation[:])

	txBytes, err := utxodb.MakeSimpleTransferTransaction(
		par.WithAmount(allowanceRequestFee).
			WithTargetLock(&ledger.TagAlongLock{
				TargetSequencerID: td.target,
				SenderID:          base.HolderID(sender),
			}).
			// appended in order: request data lands at element 3, the
			// ensureStopDelegation at 4, which is where the delegate lock
			// looks for the allowance
			WithConstraintBinary(easyfl.InlineDataBytecode(reqParams.Bytes())).
			WithConstraint(&ledger.EnsureStopDelegation{ChainID: namedDelegation, Allowance: allowance}),
	)
	require.NoError(td, err)
	require.NoError(td, td.u.AddTransaction(txBytes))

	// locate the produced tag-along output in the sequencer's backlog
	outs, err := td.u.SugaredStateReader().GetTagAlongBacklogForSequencer(td.target)
	require.NoError(td, err)
	for i := range outs {
		if _, idx := outs[i].Output.EnsureStopDelegationConstraint(); idx == 4 {
			ret := outs[i]
			return &ret
		}
	}
	require.Fail(td, "allowance request output not found")
	return nil
}

type allowanceRevokeParams struct {
	ts base.LedgerTime
	// request output carrying the allowance; nil means no allowance at all
	request *ledger.OutputWithID
	// how much to actually take out of the delegation balance
	take uint64
	// omit the third unlock byte even though a request output is consumed
	omitUnlockRef bool
	// unlock the delegation as master rather than target
	unlockAsMaster bool
	prntx          bool
}

// revokeDelegationWithAllowance builds the askstop transaction: seq chain at
// input 0, delegation at input 1, request output (if any) at input 2. The
// produced delegation goes on hold with its balance reduced by `take`, and
// the sequencer chain absorbs the fee plus whatever was taken.
func (td *testData) revokeDelegationWithAllowance(par allowanceRevokeParams) error {
	txb := exhelp.New()

	_, _, err := txb.ConsumeOutputsNoUnlock(&td.seqChainOrigin.OutputWithID)
	require.NoError(td, err)

	predIdx, err := txb.ConsumeOutput(td.delegatedOutput.Output, td.delegatedOutput.ID)
	require.NoError(td, err)

	requestIdx := byte(0xff)
	absorbed := uint64(0)
	if par.request != nil {
		requestIdx, err = txb.ConsumeOutput(par.request.Output, par.request.ID)
		require.NoError(td, err)
		absorbed = par.request.Output.TokenBalance()
	}

	// sequencer successor absorbs the fee and the amount taken from the delegation
	successorChainConstraint := ledger.NewChainConstraint(td.seqChainOrigin.ChainID, 0, td.seqChainOrigin.OriginSlot, 0, 0, td.seqChainOrigin.TransitionCounter+1, 0)
	succChainIdx, err := txb.ProduceOutput(td.seqChainOrigin.Output.Clone(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(td.seqChainOrigin.Output.TokenBalance() + absorbed + par.take))
		o.PutConstraint(successorChainConstraint.Bytes(), ledger.ConstraintIndexChain)
	}))
	require.NoError(td, err)
	txb.PutSignatureUnlock(0)
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(0))

	revokedOut, err := td.delegatedOutput.MakeDelegationRevokeOutput(ledger.MakeDelegationRevokeOutputParams{
		TxTs:                     par.ts,
		PredOutputIndex:          predIdx,
		TakeFromBalance:          par.take,
		DisableConsistencyChecks: true,
	})
	require.NoError(td, err)
	revokedIdx, err := txb.ProduceOutput(revokedOut)
	require.NoError(td, err)

	// unlock the delegation, optionally referencing the request output
	if par.unlockAsMaster {
		additional := []byte{ledger.DelegationUnlockedByMaster}
		if par.request != nil && !par.omitUnlockRef {
			additional = append(additional, requestIdx)
		}
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), additional...)
	} else {
		additional := []byte{ledger.DelegationUnlockedByTarget}
		if par.request != nil && !par.omitUnlockRef {
			additional = append(additional, requestIdx)
		}
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0), additional...)
	}
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(revokedIdx))

	if par.request != nil {
		// tag-along lock is unlocked by the target chain at input 0; the
		// ensureStopDelegation at element 4 names the produced delegation
		txb.PutUnlockParams(requestIdx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0))
		txb.PutUnlockParams(requestIdx, 4, []byte{revokedIdx})
	}

	frozenCoverageDelta, err := txb.CalcFrozenCoverageDelta()
	require.NoError(td, err)
	txb.MustPutFrozenCoverage(succChainIdx, frozenCoverageDelta, par.ts)

	txb.SetSequencerData(succChainIdx, txbuildercore.SequencerOutputIndexNone)
	dummyTxId := base.NewTransactionID(par.ts.AddTicks(-5), base.TransactionIDShort{}, true)
	txb.PushEndorsements(dummyTxId)

	txb.ComputeInputCommitment()
	txb.SetTimestamp(par.ts)
	txb.SignED25519(td.seqPrivateKey)

	txBytes, _, txString, err := txbtest.BuildAndValidate(txb)
	if err != nil {
		if par.prntx {
			return fmt.Errorf("error: '%v'\n---- failing tx ----\n%s", err, txString)
		}
		return err
	}
	return td.u.AddTransaction(txBytes)
}

// setupFrozenDelegation brings the harness to the state every allowance test
// starts from: a delegation frozen for its maximum span, which is the only
// situation in which askstop — and therefore an allowance — is meaningful.
// Outside the frozen span the master simply consumes the output itself.
func setupFrozenDelegation(t *testing.T) *testData {
	td := &testData{T: t}
	td.init()

	ts := td.seqChainOrigin.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	// a non-zero inflation cut: the allowance ceiling is the unearned part of
	// the advance, so a delegation whose target advanced nothing has nothing to
	// unwind and a ceiling of 0.
	_, _, err := td.initDelegationUTXOMake(ts, 4, 900)
	require.NoError(t, err)

	// the target transits the delegation into the frozen state, prepaying the advance
	ts = td.timestampSlotsForward(100)
	err = td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
		ts:                       ts,
		freezeUntilEpoch:         td.delegatedOutput.FreezeUntilMax(ts),
		inflate:                  true,
		disableConsistencyChecks: true,
	})
	require.NoError(t, err)
	require.True(t, td.delegatedOutput.IsMarkedFrozen(), "delegation must be frozen for allowance tests")

	return td
}

// allowanceCase runs one edge case: build a request output granting
// `allowance` (optionally by a forger, optionally naming another
// delegation), then try to stop the delegation taking `take` out of it.
type allowanceCase struct {
	allowance      uint64
	take           uint64
	forgedSender   bool
	namesOther     bool
	omitUnlockRef  bool
	unlockAsMaster bool
}

func (td *testData) runAllowanceCase(c allowanceCase) error {
	signer, sender := td.masterPrivateKey, td.masterAddr
	if c.forgedSender {
		// a funded account that is NOT the delegation master
		keys, _, addrs := td.u.GenerateAddresses(10, 1)
		require.NoError(td, td.u.TokensFromFaucet(addrs[0], 100_000_000))
		signer, sender = keys[0], addrs[0]
	}
	named := td.delegatedOutput.ChainID
	if c.namesOther {
		named = base.RandomChainID()
	}

	req := td.makeAllowanceRequestOutput(td.timestampSlotsForward(1), signer, sender, named, c.allowance)

	return td.revokeDelegationWithAllowance(allowanceRevokeParams{
		ts:             td.timestampSlotsForward(2),
		request:        req,
		take:           c.take,
		omitUnlockRef:  c.omitUnlockRef,
		unlockAsMaster: c.unlockAsMaster,
	})
}

// TestAllowanceDecreaseWithin: the sequencer takes what the master
// authorised out of the delegation balance. This is the whole point of the
// feature — the delegator needs no liquid tokens of their own to stop.
func TestAllowanceDecreaseWithin(t *testing.T) {
	td := setupFrozenDelegation(t)

	ceiling := td.delegatedOutput.AllowanceCeiling()
	require.Greater(t, ceiling, uint64(0), "a frozen delegation must have a non-zero ceiling")
	allowance := ceiling / 2
	before := td.delegatedOutput.Output.TokenBalance()
	chainID := td.delegatedOutput.ChainID

	require.NoError(t, td.runAllowanceCase(allowanceCase{allowance: allowance, take: allowance}))

	after, err := td.u.SugaredStateReader().GetDelegatedOutput(chainID)
	require.NoError(t, err)
	require.EqualValues(t, before-allowance, after.Output.TokenBalance(),
		"the allowance must have come out of the delegation balance")
	require.True(t, after.IsMarkedOnHold(), "askstop still leaves the delegation on hold")
}

// TestAllowancePartialTake: the sequencer may take less than authorised.
func TestAllowancePartialTake(t *testing.T) {
	td := setupFrozenDelegation(t)

	allowance := td.delegatedOutput.AllowanceCeiling() / 2
	before := td.delegatedOutput.Output.TokenBalance()
	chainID := td.delegatedOutput.ChainID

	require.NoError(t, td.runAllowanceCase(allowanceCase{allowance: allowance, take: allowance / 3}))

	after, err := td.u.SugaredStateReader().GetDelegatedOutput(chainID)
	require.NoError(t, err)
	require.EqualValues(t, before-allowance/3, after.Output.TokenBalance())
}

// TestAllowanceEdgeCasesRejected: every way of overreaching must fail.
func TestAllowanceEdgeCasesRejected(t *testing.T) {
	for name, c := range map[string]func(ceiling uint64) allowanceCase{
		// one mote more than authorised
		"take_exceeds_allowance": func(ceiling uint64) allowanceCase {
			return allowanceCase{allowance: ceiling / 2, take: ceiling/2 + 1}
		},
		// allowance above what the sequencer actually loses: an over-generous
		// wallet must not be usable to drain a delegation
		"allowance_above_ceiling": func(ceiling uint64) allowanceCase {
			return allowanceCase{allowance: ceiling + 1, take: ceiling + 1}
		},
		// the allowance is only the master's to give
		"forged_sender": func(ceiling uint64) allowanceCase {
			return allowanceCase{allowance: ceiling / 2, take: ceiling / 2, forgedSender: true}
		},
		// an allowance for delegation X must not authorise a decrease on Y
		"names_other_delegation": func(ceiling uint64) allowanceCase {
			return allowanceCase{allowance: ceiling / 2, take: ceiling / 2, namesOther: true}
		},
		// without the third unlock byte the ordinary non-decrease rule applies,
		// even though the allowance sits right there in a consumed input
		"no_unlock_reference": func(ceiling uint64) allowanceCase {
			return allowanceCase{allowance: ceiling / 2, take: ceiling / 2, omitUnlockRef: true}
		},
		// the master grants allowances, it does not consume them
		"master_path_with_reference": func(ceiling uint64) allowanceCase {
			return allowanceCase{allowance: ceiling / 2, take: ceiling / 2, unlockAsMaster: true}
		},
		// no allowance authorised at all: the gate must be exactly as before
		"zero_allowance_forbids_decrease": func(uint64) allowanceCase {
			return allowanceCase{allowance: 0, take: 1}
		},
	} {
		t.Run(name, func(t *testing.T) {
			td := setupFrozenDelegation(t)
			err := td.runAllowanceCase(c(td.delegatedOutput.AllowanceCeiling()))
			require.Error(t, err)
			t.Logf("rejected as expected: %v", err)
		})
	}
}

// Stopping a frozen delegation early returns an advance, it does not pay a
// penalty. The target prepaid the delegator for the whole frozen span at some
// promille share; what comes back is the part of that advance the remaining
// span will no longer earn, at the same share. So the allowance ceiling must
// scale with the share pinned at freeze time, and must stay strictly below the
// uncut projection: the target absorbs its own foregone cut rather than being
// made whole for it. Charging the uncut projection would price that cut as a
// termination penalty, payable on every add-to-delegation cycle.
func TestAllowanceCeilingIsUnwindNotPenalty(t *testing.T) {
	td := setupFrozenDelegation(t)
	d := td.delegatedOutput

	// setupFrozenDelegation freezes at the delegator's required cut
	require.EqualValues(t, 900, d.AdvanceShare, "freeze pins the share it advanced at")
	require.EqualValues(t, d.RequiredInflationCut, d.AdvanceShare)

	lib := ledger.L(d.ID.Slot())
	lastSlot := lib.LastSlotInEpochDirect(d.Target, d.LastFrozenEpoch, d.EpochSlots())
	require.Greater(t, lastSlot, d.ID.Slot(), "delegation must still be inside its frozen span")

	// the uncut projection over the same span, measured from the output's own
	// slot - the quantity AllowanceCeiling used to return in full
	uncut := lib.ChainInflationMultiStep(d.Output.TokenBalance(), d.ID.Slot(), lastSlot-d.ID.Slot()+1)
	require.Greater(t, uncut, uint64(0))

	ceiling := d.AllowanceCeiling()
	require.EqualValues(t, uncut*uint64(d.AdvanceShare)/1000, ceiling, "ceiling is the advanced share of the projection")
	require.Less(t, ceiling, uncut, "the target's own foregone cut is not charged to the delegator")
}
