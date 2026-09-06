package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// TestBranchMultiBonusRejected is the targeted regression for the branch-bonus
// minting finding (audit FECON-1).
//
// On a branch transaction the chain constraint required EVERY cross-slot chain
// output to declare the full branch inflation bonus, not just the sequencer's
// own output. Since amount conservation is tx-wide, a branch carrying N extra
// chain transitions minted N extra bonuses. The guard that limits an action to
// the sequencer output (selfOutputIndex == txSequencerOutputIndex) was present
// on the branch counter but missing from the inflation rule; the fix adds it.
//
// Positive control: an honest branch (sequencer output + stem) validates and
// declares exactly one bonus. Negative: the same branch with a second cross-slot
// chain transition declaring the bonus is now rejected.
func TestBranchMultiBonusRejected(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	genPriv, _ := u.GenesisKeys()
	genAddr := ledger.SigLockFromED25519PrivateKey(genPriv)
	require.NoError(t, u.TokensFromFaucet(genAddr, 1_000_000_000))

	seqID := *u.GenesisChainID()
	stemSlot := u.SugaredStateReader().GetStemOutput().ID.Slot()

	// A plain (non-sequencer) chain owned by the genesis key. Derive its
	// timestamp from the actual funding output (not a hardcoded slot) so the
	// origin slot matches the tx slot regardless of where funding landed — the
	// ledger is a process-wide singleton other tests in this package mutate.
	genOuts, err := u.StateReader().GetUTXOsForController(genAddr.ControllerID())
	require.NoError(t, err)
	require.True(t, len(genOuts) > 0)
	fundTs := genOuts[0].ID.Timestamp()
	originTs := fundTs.AddSlots(1)
	if originTs.IsSlotBoundary() {
		originTs = originTs.AddTicks(1)
	}
	plainChain, err := u.CreateChainOrigin(genPriv, originTs, 200_000_000)
	require.NoError(t, err)

	// Branch at the slot boundary after BOTH the sequencer predecessor and the
	// plain chain, so both transitions on the branch are cross-slot.
	branchSlot := stemSlot
	if s := plainChain.ID.Slot(); s > branchSlot {
		branchSlot = s
	}
	branchTs := base.T(branchSlot+1, 0) // slot boundary -> branch transaction

	// buildBranch builds a branch extending the bootstrap sequencer. When inject
	// is true it also transitions the plain chain, declaring the branch bonus on
	// that (non-sequencer) output — the FECON-1 attack shape.
	buildBranch := func(inject bool) ([]byte, base.TransactionID, string, uint64, error) {
		rdr := u.SugaredStateReader()
		txb, err := txbuilder_seq.NewWithSequencerID(branchTs, seqID, genPriv, rdr)
		require.NoError(t, err)
		bonus := txb.BranchInflationAmount()
		require.True(t, bonus > 0, "branch must carry a bonus")

		if inject {
			cc := plainChain.Output.ChainConstraint()
			require.NotNil(t, cc)
			cIdx, err := txb.ConsumeOutput(plainChain.Output, plainChain.ID)
			require.NoError(t, err)

			// successor declaring the branch bonus as its inflation (the mint).
			succCC := ledger.NewChainConstraint(
				plainChain.ChainID, cIdx, cc.OriginSlot,
				cc.CumulativeChainInflation,    // branch arm: +(selfInflation - bonus) = +0
				cc.CumulativeBranchBonus+bonus, // branch arm: += bonus
				cc.TransitionCounter+1,
				cc.BranchCounter, // non-sequencer: branch counter unchanged
			)
			succ := plainChain.Output.Clone(func(o *ledger.OutputBuilder) {
				o.WithAmounts(int64(plainChain.Output.TokenBalance()+bonus), int64(bonus), 0)
				o.PutConstraint(succCC.Bytes(), ledger.ConstraintIndexChain)
			})
			sIdx, err := txb.ProduceOutput(succ)
			require.NoError(t, err)
			txb.PutSignatureUnlock(cIdx)
			txb.PutUnlockParams(cIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(sIdx))
		}
		b, id, s, err := txbtest.BuildAndValidate(txb)
		return b, id, s, bonus, err
	}

	// Positive control: honest branch validates, exactly one bonus.
	_, _, _, bonus, err := buildBranch(false)
	require.NoError(t, err, "honest branch must validate")
	t.Logf("honest branch bonus = %s", util.Th(bonus))

	// Negative: the multi-bonus branch must be rejected by the chain inflation
	// guard. Depending on which conjunct trips first the message is either
	// "wrong inflation amount" or "wrong cumulative chain inflation" — both are
	// the FECON-1 guard (bonus is reserved to the sequencer output on a branch).
	_, _, s, _, err := buildBranch(true)
	require.Error(t, err, "multi-bonus branch must be rejected")
	require.NoError(t, util.MustErrorWith(err, "inflation"),
		"expected the chain inflation guard to reject; got: %v\n%s", err, s)
	require.NoError(t, util.MustErrorWith(err, "chain"),
		"rejection must come from the chain constraint; got: %v\n%s", err, s)
}
