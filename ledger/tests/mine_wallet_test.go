// End-to-end test of the wallet-side mine-transition assembly used by
// `proxi node mine`. It reproduces exactly the txbuildercore build path of
// proxi/node_cmd/mine.go (parse the mine output, compose successor + payout +
// tag-along, chain-unlock, sign) and runs the result through utxodb, proving the
// singleton-free wallet assembly is accepted by the real ledger validator.
package tests

import (
	"encoding/binary"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/blake2b"
)

// walletLibFromGlobal builds a txbuildercore.Library[any] from the current
// singleton via JSON, mimicking how the wallet constructs its library at init.
func walletLibFromGlobal(t *testing.T) *txbuildercore.Library[any] {
	t.Helper()
	jsonBytes := easyfl.ToJSON(ledger.L(base.MaxSlot).Library, true, false)
	desc, err := easyfl.ReadLibraryFromJSON(jsonBytes)
	require.NoError(t, err)
	tlib, err := txbuildercore.NewLibrary(desc)
	require.NoError(t, err)
	return tlib
}

// TestMineWalletBuildPath mines one transition entirely through the wallet
// (txbuildercore) helpers and asserts utxodb accepts it and the miner ends up
// controlling the full minted A.
func TestMineWalletBuildPath(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	minerPriv, _, _ := u.GenerateAddress(7)
	minerHolderID := base.HolderIDFromED25519PrivateKey(minerPriv)
	tlib := walletLibFromGlobal(t)

	a := mineConst(t, "constMineAmount")
	p := uint32(mineConst(t, "constMineMinPace"))
	fee := a / 200

	// fetch + parse the mine chain output wallet-side (raw bytes only)
	md, err := u.StateReader().GetUTXOForChainID(base.MineChainID)
	require.NoError(t, err)
	predWithID, err := md.Parse()
	require.NoError(t, err)
	predBytes := predWithID.Output.Bytes()
	predOID := predWithID.ID

	predOut, err := txbuildercore.OutputFromBytes(predBytes)
	require.NoError(t, err)
	predML, err := tlib.ParseMineLock(predOut.MustConstraintAt(txbuildercore.ConstraintIndexLock))
	require.NoError(t, err)
	predCC, err := tlib.ParseChainConstraint(predOut.MustConstraintAt(txbuildercore.ConstraintIndexChain))
	require.NoError(t, err)
	predBalance, err := txbuildercore.DecodeTokenBalance(predBytes)
	require.NoError(t, err)
	predSlot := predOID.Timestamp().Slot

	succSlot := predSlot + p
	// difficulty K = B, independent of the step length
	k := int(predML.B)
	// the successor carries the retargeted difficulty (held here: the genesis ring
	// is still zero-seeded)
	succB := ledger.L(0).MineAdjustedB(predML.B, predML.S3, succSlot)

	// successor (index 0): balance unchanged, inflation A, R-=A, B retargeted, ring rolled
	succLockBin, err := tlib.NewMineLock(predML.R-a, succB, predSlot, predML.S1, predML.S2)
	require.NoError(t, err)
	succChainBin, err := tlib.NewChainTransition(base.MineChainID, 0, predCC.OriginSlot,
		predCC.CumulativeChainInflation+a, 0, predCC.TransitionCounter+1, 0)
	require.NoError(t, err)
	sb := txbuildercore.NewOutputBuilder()
	sb.PutConstraint(txbuildercore.EncodeAmounts(predBalance, a), txbuildercore.ConstraintIndexAmounts)
	sb.PutConstraint(succLockBin, txbuildercore.ConstraintIndexLock)
	sb.PutConstraint(succChainBin, txbuildercore.ConstraintIndexChain)

	payoutOut, err := txbuildercore.NewSigLockOutput(tlib, a-fee, minerHolderID)
	require.NoError(t, err)
	tagAlongOut, err := txbuildercore.NewTagAlongOutput(tlib, fee, *u.GenesisChainID(), minerHolderID)
	require.NoError(t, err)

	txb := txbuildercore.New(0)
	predIdx := txb.ConsumeOutput(predBytes, predOID)
	txb.ProduceOutput(sb.Output().Bytes())
	txb.ProduceOutput(payoutOut.Bytes())
	txb.ProduceOutput(tagAlongOut.Bytes())
	txb.PutUnlockParams(predIdx, txbuildercore.ConstraintIndexChain, txbuildercore.ChainUnlockParams(0))
	txb.SetTimestamp(base.T(succSlot, 1))
	txb.ComputeInputCommitment()

	// PoW: nonce in the open lock's unlock params, re-sign each attempt
	var nonce [8]byte
	var txBytes []byte
	for n := uint64(0); ; n++ {
		binary.BigEndian.PutUint64(nonce[:], n)
		txb.PutUnlockParams(predIdx, txbuildercore.ConstraintIndexLock, nonce[:])
		txb.SignED25519(minerPriv)
		txBytes = txb.Bytes()
		if trailingZeroBits(blake2b.Sum256(txBytes)) >= k {
			break
		}
	}

	require.NoError(t, u.AddTransaction(txBytes))

	// the miner controls the whole minted A (payout A-T + reclaimable tag-along T)
	minerLock := ledger.SigLockFromED25519PrivateKey(minerPriv)
	require.EqualValues(t, a, u.Balance(minerLock))
}
