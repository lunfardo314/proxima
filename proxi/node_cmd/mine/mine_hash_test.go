package mine

import (
	"encoding/binary"
	"math/bits"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// Transaction-hash work (kb/library_upgrade6_mining.md): the miner fixes the transit
// before the search, keeps the hash state of its ID preimage up to the nonce, and the
// workers vary only the nonce. A solution, signed, is a transit whose ID carries the
// work found, and the miner's own verification accepts it.

// hashFixture is the verification fixture under the rules of library upgrade 6.
func hashFixture(t *testing.T) *verifyFixture {
	f := newVerifyFixture(t, 6)
	v6 := *f.m.consts
	v6.UpgradeIndex = txbuildercore.TxVersionSignerBoundID
	v6.MinerVersion = 6
	f.m.consts = &v6
	return f
}

func TestMineHashWorkSolvesAndVerifies(t *testing.T) {
	for name, payout := range map[string]func(f *verifyFixture, slot uint32) minePayout{
		"full reward as one tag-along": func(f *verifyFixture, slot uint32) minePayout {
			return f.m.fullPayoutTo(f.m.tagAlongSeqID, slot)
		},
		"a new delegation and the inclusion fee": func(f *verifyFixture, slot uint32) minePayout {
			return minePayout{option: txbuildercore.MinePayoutDelegation, minted: f.m.consts.MineAmountAtSlot(slot), target: f.m.tagAlongSeqID}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := hashFixture(t)
			f.m.workers = 4
			succSlot, succB, succC := f.honestTarget()
			p := payout(f, succSlot)
			txb, _ := f.m.buildTransit(f.pred, succSlot, succB, succC, p)
			ht, err := newHashTemplate(txb, f.m.holderID)
			require.NoError(t, err)

			k := int(f.m.consts.MineRequiredK(f.pred.ml.B, uint64(succSlot-f.pred.oid.Timestamp().Slot)))
			var sol *mineSolution
			f.m.mineParallel(f.pred.oid, succSlot, k, time.Now().Add(30*time.Second), func() []byte { return nil },
				func(s mineSolution) {
					sol = &s
					f.m.abort.Store(true)
				}, ht)
			require.NotNil(t, sol)
			require.Nil(t, sol.proof, "no VRF proof under hash work")

			// the solution as the round submits it: the template with its nonce, signed
			txb, _ = f.m.buildTransit(f.pred, succSlot, succB, succC, p)
			txb.TxData.TxConstraints = [][]byte{mineNonceBytecode(sol.nonce)}
			txb.SignED25519(f.m.wallet.PrivateKey)
			id, err := txbuildercore.TxIDFromBytes(txb.Bytes())
			require.NoError(t, err)
			require.Equal(t, id[24:], sol.work[:8], "the signed transit's ID carries the work found")
			require.GreaterOrEqual(t, bits.TrailingZeros64(binary.BigEndian.Uint64(id[24:])), k)

			tip, err := verifyMineTransit(f.m.lib, f.m.constsAt, f.pred, txb.Bytes())
			require.NoError(t, err)
			require.Equal(t, sol.work, tip.work, "the tree ranks the transit by the same work")
			require.Equal(t, f.pred.cc.TransitionCounter+1, tip.cc.TransitionCounter)
		})
	}
}

// A transit with too little work is refused by the miner's verification under hash work,
// as by the ledger.
func TestMineHashWorkRefusesTooLittleWork(t *testing.T) {
	f := hashFixture(t)
	succSlot, succB, succC := f.honestTarget()
	k := int(f.m.consts.MineRequiredK(f.pred.ml.B, uint64(succSlot-f.pred.oid.Timestamp().Slot)))
	for n := uint64(0); ; n++ {
		txb, _ := f.m.buildTransit(f.pred, succSlot, succB, succC, f.m.fullPayoutTo(f.m.tagAlongSeqID, succSlot))
		var nonce [txbuildercore.MineNonceLen]byte
		binary.BigEndian.PutUint64(nonce[:], n)
		txb.TxData.TxConstraints = [][]byte{mineNonceBytecode(nonce)}
		txb.SignED25519(f.m.wallet.PrivateKey)
		id, err := txbuildercore.TxIDFromBytes(txb.Bytes())
		require.NoError(t, err)
		if bits.TrailingZeros64(binary.BigEndian.Uint64(id[24:])) >= k {
			continue
		}
		_, err = verifyMineTransit(f.m.lib, f.m.constsAt, f.pred, txb.Bytes())
		require.ErrorContains(t, err, "insufficient proof of work")
		return
	}
}
