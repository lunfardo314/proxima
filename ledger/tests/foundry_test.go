// Foundry tests: the constraint's own rules, plus the sigLock-controller
// guard `proxi node foundry create` composes on top of them.
//
// Three groups, in the order the foundry constraint enforces them:
//
//  1. Position immutability — a chain that carries a foundry at origin is
//     a foundry chain for life: the constraint cannot be dropped, moved,
//     or replaced, and an origin cannot put it anywhere but slot 4.
//
//  2. Supply — 0 at chain origin; on a transit it may differ from the
//     chain predecessor's only under a tx-level token(...) declaration
//     for this foundry's own tag pointing at this output, named by index
//     in TxConstraints in the predecessor's foundry unlock params. The
//     delta is accounted only inside evalToken and the closing balance
//     equation covers only declared tags, so an undeclared transit is
//     outside the balance check entirely and the constraint has to be
//     the one to refuse a supply change there.
//
//  3. Delegation ban — an inline guard script, composed from existing
//     library symbols at foundry-creation time, that pins the controller
//     lock to a sigLock across every transit.
//
// The mint / burn / retire / policy lifecycle and the token + tokenAmount
// balance machinery live in native_token_test.go, which also holds the
// shared foundryTestEnv helpers used here. Delegating a foundry chain —
// the allowed path, and what the delegation target may do to the foundry
// once it is delegated — is covered in delegate_test.go.

package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil/txbtest"
	"github.com/stretchr/testify/require"
)

// ==========================================================================
// 1. Constraint position immutability
//
// The supply arg is allowed to change (mint/burn legitimately move it, under
// the rules of group 2), so the in-EasyFL self-lock on the consumed side
// checks the SYMBOL only (#foundry), not byte-equality.
//
// Delegating a foundry chain (lock swap + delegateLockState append, foundry
// preserved at slot 4) is the canonical "still allowed" path; it is covered
// by TestDelegateFoundryChainNoPolicy / NonDestructible in
// delegate_test.go and we deliberately do not duplicate it.
// ==========================================================================

// transitFoundryDropping drops the foundry constraint on the successor
// (PutConstraint with empty bytecode at the same slot). Should be
// rejected by foundry()'s self-lock — parseBytecode panics because the
// successor's slot at foundryConstraintIndex isn't a foundry call.
func TestFoundryConstraintCannotBeDroppedOnTransit(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 500_000_000, nil)

	fIn := e.foundryInputData(t, chainID)
	chainIN, err := ledger.OutputFromBytesWithLib(fIn.Data, ledger.L(fIn.ID.Slot()))
	require.NoError(t, err)
	cc := chainIN.ChainConstraint()
	require.NotNil(t, cc)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIN, fIn.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(predIdx)

	successor := ledger.NewChainConstraint(
		fIn.ChainID, predIdx, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
		cc.TransitionCounter+1, cc.BranchCounter,
	)
	// Build a successor that intentionally drops the foundry: replace
	// slot 4 with the empty bytecode placeholder.
	chainOut := chainIN.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(successor.Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(nil, ledger.ConstraintIndexFoundry)
	})
	prodIdx, err := txb.ProduceOutput(chainOut)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(prodIdx))

	ts := fIn.ID.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "dropping foundry constraint on transit must be rejected")
	// Successor's slot 4 is empty bytecode → parseBytecode panics with
	// "unexpected EOF" before it can even check the call prefix. Either
	// EOF or a prefix mismatch is an acceptable rejection; both flow
	// through evalParseBytecode.
	require.NoError(t, util.MustErrorWith(err, "evalParseBytecode"))
}

// The successor carries a non-foundry call at slot 4 (an amounts
// constraint as a stand-in). parseBytecode in the consumed-side foundry
// check rejects it with "unexpected call prefix 'amounts'". This makes
// the symbol-check failure mode distinct from the empty-bytecode case
// covered by TestFoundryConstraintCannotBeDroppedOnTransit.
func TestFoundryConstraintCannotBeReplacedByOtherConstraint(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 500_000_000, nil)

	fIn := e.foundryInputData(t, chainID)
	chainIN, err := ledger.OutputFromBytesWithLib(fIn.Data, ledger.L(fIn.ID.Slot()))
	require.NoError(t, err)
	cc := chainIN.ChainConstraint()
	require.NotNil(t, cc)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIN, fIn.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(predIdx)

	successor := ledger.NewChainConstraint(
		fIn.ChainID, predIdx, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
		cc.TransitionCounter+1, cc.BranchCounter,
	)
	// Replace slot 4 with a real EasyFL function call that isn't a
	// foundry — a chain-origin bytecode works (prefix == #chain).
	// parseBytecode then asserts the prefix matches #foundry → fails
	// with "unexpected call prefix 'chain'".
	notFoundry := ledger.NewChainOrigin(cc.OriginSlot).Bytes()
	chainOut := chainIN.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(successor.Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(notFoundry, ledger.ConstraintIndexFoundry)
	})
	prodIdx, err := txb.ProduceOutput(chainOut)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(prodIdx))

	ts := fIn.ID.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "replacing foundry with another call at slot 4 must be rejected")
	require.NoError(t, util.MustErrorWith(err, "unexpected call prefix"))
}

// Creating a foundry origin output whose foundry constraint sits at the
// wrong slot must be rejected: foundry() on the produced side requires
// `selfBlockIndex == foundryConstraintIndex`.
func TestFoundryOriginAtWrongSlotRejected(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	outs := getSourceOutputs(t, e.u, e.addr)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	txb := exhelp.New()
	_, inTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	ts = base.MaximumTime(inTs, ts)
	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}

	// Manually build a foundry-bearing origin output but place foundry
	// at slot 5 (foundryPolicyConstraintIndex) while slot 4 is empty.
	const foundryOnChain = uint64(500_000_000)
	badOrigin := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(foundryOnChain)).WithLock(e.addr)
		o.PutConstraint(ledger.NewChainOrigin(ts.Slot).Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(nil, ledger.ConstraintIndexFoundry) // slot 4 empty
		o.PutConstraint(ledger.NewFoundry(0).Bytes(), ledger.ConstraintIndexFoundryPolicy)
	})
	_, err = txb.ProduceOutput(badOrigin)
	require.NoError(t, err)
	addRemainderIfNeeded(t, txb, e.addr)

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "foundry origin at wrong slot must be rejected")
	require.NoError(t, util.MustErrorWith(err, "foundry must be at foundryConstraintIndex"))
}

// ==========================================================================
// 2. Supply: what a transit may do to it, with and without a declaration
//
// The tests below walk every combination: origin with / without supply,
// transit with / without a supply change, and — where a supply does change —
// every way the declaration can be wrong (absent, unpointed-to, sentinel,
// dangling index, not a token call, borrowed from another foundry in the
// same tx).
// ==========================================================================

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// transitFoundry hand-builds a foundry transit: consumes the foundry
// chain output, produces the successor with supply set to newSupply, and
// optionally produces a tokenAmount(chainID, tokenOut) UTXO to the
// wallet. `wire` (may be nil) is where a test pushes tx-level
// constraints and writes the predecessor's foundry unlock params; it
// receives the consumed and produced indices of the foundry.
// Funding inputs and the base-token remainder are added afterwards.
// Returns the validation/submission error (nil on success).
func (e *foundryTestEnv) transitFoundry(
	t *testing.T,
	chainID base.ChainID,
	newSupply uint64,
	tokenOut uint64,
	wire func(txb *exhelp.Builder, predIdx, prodIdx byte),
) error {
	t.Helper()
	in := e.foundryInputData(t, chainID)
	chainIN := parseOutput(t, in)
	cc := chainIN.ChainConstraint()
	require.NotNil(t, cc)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIN, in.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(predIdx)

	successor := ledger.NewChainConstraint(
		chainID, predIdx, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
		cc.TransitionCounter+1, cc.BranchCounter,
	)
	chainOut := chainIN.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(successor.Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(ledger.NewFoundry(newSupply).Bytes(), ledger.ConstraintIndexFoundry)
	})
	prodIdx, err := txb.ProduceOutput(chainOut)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(prodIdx))

	if tokenOut > 0 {
		tokOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(100_000_000).WithLock(e.addr).WithTokenAmount(chainID, tokenOut)
		})
		require.NoError(t, tokOut.EnoughAmountForStorageDeposit())
		_, err = txb.ProduceOutput(tokOut)
		require.NoError(t, err)
	}
	if wire != nil {
		wire(txb, predIdx, prodIdx)
	}

	ts := in.ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	ts = base.MaximumTime(ts, e.appendExtraFunding(t, txb, predIdx))
	addRemainderIfNeeded(t, txb, e.addr)

	_, _, err = e.finishAndSubmit(t, txb, ts)
	return err
}

// silentTransitFoundry rewrites the supply with no declaration at all.
func (e *foundryTestEnv) silentTransitFoundry(t *testing.T, chainID base.ChainID, newSupply uint64) error {
	t.Helper()
	return e.transitFoundry(t, chainID, newSupply, 0, nil)
}

// declareAndPoint is the correct wiring: push token(chainID, prodIdx) and
// name its index in the predecessor's foundry unlock params.
func declareAndPoint(chainID base.ChainID) func(*exhelp.Builder, byte, byte) {
	return func(txb *exhelp.Builder, predIdx, prodIdx byte) {
		declIdx := byte(len(txb.TxData.TxConstraints))
		txb.PushTxConstraint(ledger.TokenFoundryBytecode(chainID, prodIdx))
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexFoundry, []byte{declIdx})
	}
}

// foundrySupply reads the current supply off the foundry chain output.
func (e *foundryTestEnv) foundrySupply(t *testing.T, chainID base.ChainID) uint64 {
	t.Helper()
	parsed, err := e.u.SugaredStateReader().GetChainOutputWithChainID(chainID)
	require.NoError(t, err)
	f, err := ledger.FoundryFromBytes(mustConstraintAt(t, parsed.Output, ledger.ConstraintIndexFoundry))
	require.NoError(t, err)
	return f.Supply
}

// walletTokenSum sums all tokenAmount(chainID, _) held by the test wallet.
// Counts every instance on every output, the way the ledger does — an
// output may legitimately carry more than one for the same tag.
func (e *foundryTestEnv) walletTokenSum(t *testing.T, chainID base.ChainID) uint64 {
	t.Helper()
	var sum uint64
	for _, o := range getSourceOutputs(t, e.u, e.addr) {
		for _, raw := range o.Output.ConstraintsRawBytes() {
			if ta, err := ledger.TokenAmountFromBytes(raw); err == nil && ta.Tag == chainID {
				sum += ta.Amount
			}
		}
	}
	return sum
}

// --------------------------------------------------------------------------
// Origin: the supply must start at 0
// --------------------------------------------------------------------------

// A foundry origin declaring a non-zero supply would claim circulating
// tokens that no transaction ever produced — and it cannot be declared,
// since the tag does not exist yet (the chain ID is still the origin ID).
// Origin at supply 0 is the ordinary path, covered by
// TestFoundryOriginNoPolicy.
func TestFoundryOriginSupplyMustBeZero(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	outs := getSourceOutputs(t, e.u, e.addr)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}

	txb := exhelp.New()
	_, inTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	ts = base.MaximumTime(inTs, ts)
	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}

	origin := exhelp.MakeFoundryOriginOutput(500_000_000, e.addr, ts.Slot, 999_999_999, nil)
	_, err = txb.ProduceOutput(origin)
	require.NoError(t, err)
	addRemainderIfNeeded(t, txb, e.addr)

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, _, err = txbtest.BuildAndValidate(txb)
	require.Error(t, err, "a foundry origin must not be able to declare a supply out of nothing")
	require.NoError(t, util.MustErrorWith(err, "foundry origin supply must be zero"))
}

// A chain that is not a foundry cannot become one mid-life: the produced
// foundry reads its predecessor's supply through the #foundry prefix, so
// the predecessor must be a foundry too. Every foundry therefore starts
// at its own chain origin, at supply 0. (Adopting a foundry at supply 0
// would be harmless in itself, but allowing it buys nothing and the
// uniform rule is simpler.)
func TestPlainChainCannotGrowAFoundry(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)

	// Plain chain origin: chain constraint at slot 3, nothing at slot 4.
	outs := getSourceOutputs(t, e.u, e.addr)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb := exhelp.New()
	_, inTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	ts = base.MaximumTime(inTs, ts)
	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}
	chainOrigin := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(500_000_000).WithLock(e.addr)
		o.PutConstraint(ledger.NewChainOrigin(ts.Slot).Bytes(), ledger.ConstraintIndexChain)
	})
	chainIdx, err := txb.ProduceOutput(chainOrigin)
	require.NoError(t, err)
	addRemainderIfNeeded(t, txb, e.addr)
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)
	txBytes, txid, failedTx, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "plain chain origin must validate: %s", failedTx)
	require.NoError(t, e.u.AddTransaction(txBytes))

	originOid, err := base.NewOutputID(txid, chainIdx)
	require.NoError(t, err)
	chainID := base.MakeOriginChainID(originOid)

	// Transit it into an output that carries foundry(1_000_000) at slot 4.
	in := e.foundryInputData(t, chainID)
	chainIN := parseOutput(t, in)
	cc := chainIN.ChainConstraint()
	require.NotNil(t, cc)

	txb2 := exhelp.New()
	predIdx, err := txb2.ConsumeOutput(chainIN, in.ID)
	require.NoError(t, err)
	txb2.PutSignatureUnlock(predIdx)
	successor := ledger.NewChainConstraint(
		chainID, predIdx, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
		cc.TransitionCounter+1, cc.BranchCounter,
	)
	grown := chainIN.Clone(func(o *ledger.OutputBuilder) {
		o.PutConstraint(successor.Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(ledger.NewFoundry(1_000_000).Bytes(), ledger.ConstraintIndexFoundry)
	})
	prodIdx, err := txb2.ProduceOutput(grown)
	require.NoError(t, err)
	txb2.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(prodIdx))

	ts2 := in.ID.Timestamp().AddSlots(1)
	if ts2.IsSlotBoundary() {
		ts2 = ts2.AddTicks(1)
	}
	ts2 = base.MaximumTime(ts2, e.appendExtraFunding(t, txb2, predIdx))
	addRemainderIfNeeded(t, txb2, e.addr)

	_, _, err = e.finishAndSubmit(t, txb2, ts2)
	require.Error(t, err, "a plain chain must not be able to grow a foundry with a supply")
	// The predecessor has nothing at slot 4, so the read panics before the
	// #foundry prefix can even be compared; either way it is the foundry
	// constraint on the produced side that refuses the transit.
	require.NoError(t, util.MustErrorWith(err, "constraint 'foundry' failed"))
}

// --------------------------------------------------------------------------
// Transit without a declaration
// --------------------------------------------------------------------------

// A transit that leaves the supply untouched is a legitimate operation
// (moving the foundry chain, re-locking it, a sequencer transiting a
// delegated foundry chain) and needs no token(...) declaration. This is
// the calibration case: the rule must keep it valid, and must not even
// look at the unlock params, which such a transit does not write.
func TestFoundryUndeclaredTransitUnchangedSupplyAccepted(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)
	mintToSelf(t, e, chainID, mintAmount)

	require.NoError(t, e.silentTransitFoundry(t, chainID, mintAmount),
		"transit that does not change the supply must stay valid without a token(...) declaration")
	require.EqualValues(t, mintAmount, e.foundrySupply(t, chainID))
}

// Inflating the supply with no token(...) declaration mints supply out of
// thin air: the foundry then claims more circulating tokens than were
// ever produced.
func TestFoundryUndeclaredSupplyInflationRejected(t *testing.T) {
	const mintAmount = uint64(1_000_000)
	const inflatedTo = uint64(1_000_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)
	mintToSelf(t, e, chainID, mintAmount)

	err := e.silentTransitFoundry(t, chainID, inflatedTo)
	require.Error(t, err,
		"raising the foundry supply from %d to %d without a token(...) declaration must be rejected; supply is now %d",
		mintAmount, inflatedTo, e.foundrySupply(t, chainID))
	require.NoError(t, util.MustErrorWith(err, "foundry supply change requires token declaration index in unlock parameters"))
}

// The same hole in the other direction is the dangerous one: zeroing the
// supply without burning lets the owner mint the whole amount again, so
// circulating tokenAmount exceeds the supply the foundry records.
func TestFoundryUndeclaredSupplyDeflationRejected(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)
	mintToSelf(t, e, chainID, mintAmount)

	err := e.silentTransitFoundry(t, chainID, 0)
	if err == nil {
		// Carry the exploit through to its consequence so the failure
		// names the broken invariant rather than just the tx.
		require.EqualValues(t, 0, e.foundrySupply(t, chainID))
		mintToSelf(t, e, chainID, mintAmount)
		t.Fatalf("undeclared transit zeroed the supply without burning: %d tokens circulate against recorded supply %d",
			e.walletTokenSum(t, chainID), e.foundrySupply(t, chainID))
	}
	require.NoError(t, util.MustErrorWith(err, "foundry supply change requires token declaration index in unlock parameters"))
	require.EqualValues(t, mintAmount, e.foundrySupply(t, chainID))
}

// foundryNonDestructible gates retirement on "consumed supply == 0". A
// free path to supply 0 would make the policy bypassable: the foundry
// retired while its tokens still circulate.
func TestFoundryUndeclaredDeflationBypassesNonDestructible(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, ledger.FoundryNonDestructibleBytecode())
	mintToSelf(t, e, chainID, mintAmount)

	// Control: while the supply is non-zero the policy does block retirement.
	require.Error(t, tryRetireFoundry(t, e, chainID),
		"foundryNonDestructible must block retirement at non-zero supply")

	if err := e.silentTransitFoundry(t, chainID, 0); err == nil {
		require.Error(t, tryRetireFoundry(t, e, chainID),
			"foundryNonDestructible bypassed: supply silently zeroed, foundry retired with %d tokens still circulating",
			e.walletTokenSum(t, chainID))
		return
	}
	// The supply could not be zeroed, so the policy still holds.
	require.EqualValues(t, mintAmount, e.foundrySupply(t, chainID))
	require.Error(t, tryRetireFoundry(t, e, chainID))
}

// --------------------------------------------------------------------------
// Transit with a declaration
// --------------------------------------------------------------------------

// The correct shape, hand-built: token(tag, prodIdx) pushed, its index
// named in the predecessor's foundry unlock params, minted tokenAmount
// produced. Supply and circulation move together.
func TestFoundryDeclaredMintAccepted(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)

	require.NoError(t, e.transitFoundry(t, chainID, mintAmount, mintAmount, declareAndPoint(chainID)),
		"a declared mint pointing at its own foundry must validate")
	require.EqualValues(t, mintAmount, e.foundrySupply(t, chainID))
	require.EqualValues(t, mintAmount, e.walletTokenSum(t, chainID))
}

// A declaration on a transit that does not move the supply is a no-op
// (delta 0, no tokenAmount on either side) and must stay valid — this is
// what TransitFoundry emits unconditionally.
func TestFoundryDeclaredTransitUnchangedSupplyAccepted(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)
	mintToSelf(t, e, chainID, mintAmount)

	require.NoError(t, e.transitFoundry(t, chainID, mintAmount, 0, declareAndPoint(chainID)),
		"a declared transit with delta 0 must validate")
	require.EqualValues(t, mintAmount, e.foundrySupply(t, chainID))
}

// The declaration is present and the tx is perfectly balanced — only the
// unlock params are missing. It must still be rejected: the foundry
// itself has to point at the declaration, otherwise "declaration present
// somewhere in the tx" would be the rule, and a second foundry could ride
// on it (see TestFoundryCrossWiredDeclarationRejected).
func TestFoundryMintWithoutUnlockParamsRejected(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)

	err := e.transitFoundry(t, chainID, mintAmount, mintAmount,
		func(txb *exhelp.Builder, _, prodIdx byte) {
			txb.PushTxConstraint(ledger.TokenFoundryBytecode(chainID, prodIdx))
		})
	require.Error(t, err, "a mint whose foundry does not name its declaration must be rejected")
	require.NoError(t, util.MustErrorWith(err, "foundry supply change requires token declaration index in unlock parameters"))
}

// A pure-conservation sentinel `token(tag, 0xFF)` declares the tag with
// delta 0 — it cannot cover a supply change. Pointing the foundry at one
// must be rejected on the foundryProducedIdx check, not accepted because
// "a token(...) for my tag exists".
func TestFoundrySupplyChangeUnderSentinelDeclarationRejected(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)
	mintToSelf(t, e, chainID, mintAmount)

	err := e.transitFoundry(t, chainID, 0, 0, func(txb *exhelp.Builder, predIdx, _ byte) {
		declIdx := byte(len(txb.TxData.TxConstraints))
		txb.PushTxConstraint(ledger.TokenSentinelBytecode(chainID))
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexFoundry, []byte{declIdx})
	})
	require.Error(t, err, "a sentinel declaration must not authorise a supply change")
	require.NoError(t, util.MustErrorWith(err, "token declaration does not point to this foundry"))
}

// Unlock params naming a TxConstraints index that does not exist must be
// rejected rather than read as "no declaration needed".
func TestFoundrySupplyChangeWithDanglingDeclarationIndexRejected(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)
	mintToSelf(t, e, chainID, mintAmount)

	err := e.transitFoundry(t, chainID, 0, 0, func(txb *exhelp.Builder, predIdx, _ byte) {
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexFoundry, []byte{7}) // nothing pushed at all
	})
	require.Error(t, err, "a declaration index pointing nowhere must be rejected")
	require.NoError(t, util.MustErrorWith(err, "evalAtPath"))
}

// Unlock params naming a tx constraint that is not a token(...) call at
// all: the #token prefix check rejects it.
func TestFoundrySupplyChangeUnderNonTokenConstraintRejected(t *testing.T) {
	const mintAmount = uint64(1_000_000)

	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createFoundryOrigin(t, 200_000_000, nil)
	mintToSelf(t, e, chainID, mintAmount)

	// An inline-data literal is a valid (always-true) tx-level constraint
	// but carries no call prefix to match #token.
	_, _, notAToken, err := ledger.L(base.MaxSlot).CompileExpression("0x01")
	require.NoError(t, err)

	err = e.transitFoundry(t, chainID, 0, 0, func(txb *exhelp.Builder, predIdx, _ byte) {
		declIdx := byte(len(txb.TxData.TxConstraints))
		txb.PushTxConstraint(notAToken)
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexFoundry, []byte{declIdx})
	})
	require.Error(t, err, "a non-token tx constraint must not authorise a supply change")
	require.NoError(t, util.MustErrorWith(err, "evalParseBytecode"))
}

// Cross-wiring: two foundries transit in the same tx, only foundry A is
// declared (a legitimate mint of A), and foundry B points its unlock
// params at A's declaration to change B's supply for free. B's supply
// change is covered by nothing, so the tag check must catch it.
func TestFoundryCrossWiredDeclarationRejected(t *testing.T) {
	const mintA = uint64(1)
	const inflatedB = uint64(1_000_000)

	e := newFoundryTestEnv(t, 20_000_000_000)
	chainA := e.createFoundryOrigin(t, 200_000_000, nil)
	chainB := e.createFoundryOrigin(t, 200_000_000, nil)

	inA := e.foundryInputData(t, chainA)
	inB := e.foundryInputData(t, chainB)
	outA, outB := parseOutput(t, inA), parseOutput(t, inB)

	txb := exhelp.New()
	// Foundry A at input 0 (signature), foundry B at input 1 (reference to 0).
	idxA, err := txb.ConsumeOutput(outA, inA.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(idxA)
	idxB, err := txb.ConsumeOutput(outB, inB.ID)
	require.NoError(t, err)
	require.NoError(t, txb.PutUnlockReference(idxB, ledger.ConstraintIndexLock, idxA))

	transit := func(in *ledger.Output, chainID base.ChainID, predIdx byte, newSupply uint64) byte {
		cc := in.ChainConstraint()
		require.NotNil(t, cc)
		successor := ledger.NewChainConstraint(
			chainID, predIdx, cc.OriginSlot,
			cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
			cc.TransitionCounter+1, cc.BranchCounter,
		)
		out := in.Clone(func(o *ledger.OutputBuilder) {
			o.PutConstraint(successor.Bytes(), ledger.ConstraintIndexChain)
			o.PutConstraint(ledger.NewFoundry(newSupply).Bytes(), ledger.ConstraintIndexFoundry)
		})
		prodIdx, err := txb.ProduceOutput(out)
		require.NoError(t, err)
		txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(prodIdx))
		return prodIdx
	}

	// A legitimately mints (its first transit); B inflates for free.
	prodA := transit(outA, chainA, idxA, mintA)
	transit(outB, chainB, idxB, inflatedB)

	// Only A is declared. Both foundries point at that one declaration.
	declIdx := byte(len(txb.TxData.TxConstraints))
	txb.PushTxConstraint(ledger.TokenFoundryBytecode(chainA, prodA))
	txb.PutUnlockParams(idxA, ledger.ConstraintIndexFoundry, []byte{declIdx})
	txb.PutUnlockParams(idxB, ledger.ConstraintIndexFoundry, []byte{declIdx})

	// A's mint needs a matching tokenAmount(chainA, mintA) output to balance.
	minted := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(100_000_000).WithLock(e.addr).WithTokenAmount(chainA, mintA)
	})
	require.NoError(t, minted.EnoughAmountForStorageDeposit())
	_, err = txb.ProduceOutput(minted)
	require.NoError(t, err)

	ts := base.MaximumTime(inA.ID.Timestamp(), inB.ID.Timestamp()).AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	ts = base.MaximumTime(ts, e.appendExtraFunding(t, txb, idxA))
	addRemainderIfNeeded(t, txb, e.addr)

	_, _, err = e.finishAndSubmit(t, txb, ts)
	require.Error(t, err, "foundry B must not ride on foundry A's token declaration")
	require.NoError(t, util.MustErrorWith(err, "token declaration is for another tag"))
	// And nothing landed: both foundries are still at their origin supply.
	require.EqualValues(t, 0, e.foundrySupply(t, chainA))
	require.EqualValues(t, 0, e.foundrySupply(t, chainB))
}

// ==========================================================================
// 3. Delegation ban via an inline sigLock-controller guard
//
// `proxi node foundry create` (without --allow_delegation) appends an inline
// guard script after foundry(). The guard self-locks at its own position
// across every transit and requires the controller lock (lockConstraintIndex)
// to stay a sigLock on every produced foundry output. Swapping the controller
// to a non-sigLock (e.g. a delegateLock — i.e. delegating the foundry) is
// rejected; changing it to a DIFFERENT sigLock is still allowed.
//
// Nothing in the ledger library is modified — the guard is composed entirely
// from existing library symbols and compiled at foundry-creation time. These
// tests compile the same source and exercise it through real transitions.
//
// The end-to-end "delegate a guarded foundry is rejected" path (the actual
// `proxi node dlg chain` build) is verified live against a standalone node;
// here we prove the guard mechanics directly.
// ==========================================================================

// foundrySigLockGuardSource MUST stay byte-identical to the constant of the
// same name in proxi/node_cmd/foundry/create.go.
const foundrySigLockGuardSource = "and(selfImmutableOnSuccessorIndex(selfBlockIndex),or(not(selfIsProducedOutput),require(equal(parseBytecode(selfSiblingConstraint(lockConstraintIndex),0x),#sigLock),!!!foundry_expects_siglock)))"

func compileFoundryGuard(t *testing.T) []byte {
	t.Helper()
	_, _, code, err := ledger.L(base.MaxSlot).CompileExpression(foundrySigLockGuardSource)
	require.NoError(t, err)
	return code
}

// createGuardedFoundryOrigin builds and submits a foundry origin with the
// sigLock-controller guard appended after foundry() (index 5, since no
// predefined policy is attached). Returns the future chain ID.
func (e *foundryTestEnv) createGuardedFoundryOrigin(t *testing.T, onChainAmount uint64) base.ChainID {
	t.Helper()
	guard := compileFoundryGuard(t)

	outs := getSourceOutputs(t, e.u, e.addr)
	ts := outs[0].ID.Timestamp().AddSlots(1)
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(1)
	}
	txb := exhelp.New()
	_, inTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	ts = base.MaximumTime(inTs, ts)
	for i := range outs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
	}

	foundryOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(onChainAmount)).WithLock(e.addr)
		o.PutConstraint(ledger.NewChainOrigin(ts.Slot).Bytes(), ledger.ConstraintIndexChain)
		o.PutConstraint(ledger.NewFoundry(0).Bytes(), ledger.ConstraintIndexFoundry)
		o.MustPushConstraint(guard) // appended after foundry() — index 5
	})
	require.NoError(t, foundryOut.EnoughAmountForStorageDeposit())
	foundryIdx, err := txb.ProduceOutput(foundryOut)
	require.NoError(t, err)
	addRemainderIfNeeded(t, txb, e.addr)

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	txBytes, txid, failedTx, err := txbtest.BuildAndValidate(txb)
	require.NoError(t, err, "guarded foundry-origin build/validation failed: %s", failedTx)
	require.NoError(t, e.u.AddTransaction(txBytes))

	foundryOid, err := base.NewOutputID(txid, foundryIdx)
	require.NoError(t, err)
	return base.MakeOriginChainID(foundryOid)
}

// transitFoundryControllerTo consumes the guarded foundry chain output and
// produces a successor whose controller lock (index 2) is `newController`.
// The foundry constraint at index 4 and the guard at index 5 carry over
// byte-equal. Returns the validation error (nil on success).
func (e *foundryTestEnv) transitFoundryControllerTo(t *testing.T, chainID base.ChainID, newController ledger.Lock) (string, error) {
	t.Helper()
	fIn := e.foundryInputData(t, chainID)
	chainIN, err := ledger.OutputFromBytesWithLib(fIn.Data, ledger.L(fIn.ID.Slot()))
	require.NoError(t, err)
	cc := chainIN.ChainConstraint()
	require.NotNil(t, cc)

	txb := exhelp.New()
	predIdx, err := txb.ConsumeOutput(chainIN, fIn.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(predIdx)

	successorCC := ledger.NewChainConstraint(
		fIn.ChainID, predIdx, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus,
		cc.TransitionCounter+1, cc.BranchCounter,
	)
	// Clone preserves foundry (index 4) and the guard (index 5) byte-equal;
	// only the controller (index 1 index-values + index 2 lock) and the chain
	// constraint (index 3) change.
	succ := chainIN.Clone(func(o *ledger.OutputBuilder) {
		o.WithLock(newController)
		o.PutConstraint(successorCC.Bytes(), ledger.ConstraintIndexChain)
	})
	prodIdx, err := txb.ProduceOutput(succ)
	require.NoError(t, err)
	txb.PutUnlockParams(predIdx, ledger.ConstraintIndexChain, ledger.NewChainUnlockParams(prodIdx))

	ts := fIn.ID.Timestamp().AddTicks(int(ledger.L(0).TransactionPace))
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(e.privKey)

	_, _, failedTx, err := txbtest.BuildAndValidate(txb)
	return failedTx, err
}

// The guard must allow changing the foundry controller to a DIFFERENT sigLock
// (only the lock kind is checked, not the holder). This is the explicit
// guarantee requested.
func TestFoundryGuardAllowsControllerChangeToAnotherSigLock(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createGuardedFoundryOrigin(t, 500_000_000)

	_, _, addr2 := e.u.GenerateAddress(2) // a different sigLock holder
	require.NotEqualValues(t, e.addr, addr2)

	failedTx, err := e.transitFoundryControllerTo(t, chainID, addr2)
	require.NoError(t, err, "changing the foundry controller to another sigLock must be allowed: %s", failedTx)
}

// The guard must reject swapping the foundry controller to any non-sigLock.
// A chainLock stands in for the general non-sigLock case; a delegateLock (used
// when delegating a foundry) is likewise a non-sigLock and is rejected by the
// same prefix check, which is what bans foundry delegation by default.
func TestFoundryGuardRejectsNonSigLockController(t *testing.T) {
	e := newFoundryTestEnv(t, 10_000_000_000)
	chainID := e.createGuardedFoundryOrigin(t, 500_000_000)

	notSigLock := ledger.ChainLockFromChainID(chainID) // any non-sigLock controller

	_, err := e.transitFoundryControllerTo(t, chainID, notSigLock)
	require.Error(t, err, "swapping the foundry controller to a non-sigLock must be rejected")
	// '!!!foundry_expects_siglock' surfaces with spaces at runtime.
	require.NoError(t, util.MustErrorWith(err, "foundry expects siglock"))
}
