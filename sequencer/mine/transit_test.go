package mine

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// The canonical winner rule ranks competing mine transits on one predecessor.
// The descriptors are built directly, since the rule reads nothing else off the
// transaction.

const pbcTicks = 25

// transitOf is a mine transit with the given predecessor, slot and VRF output;
// `who` makes the txid distinct.
func transitOf(pred base.OutputID, slot uint32, vrf []byte, who byte) *Transit {
	var txid base.TransactionID
	copy(txid[:base.LedgerTimeByteLength], base.T(slot, 1).Bytes())
	txid[len(txid)-1] = who
	return &Transit{Pred: pred, Slot: slot, VRFOutput: vrf, TxID: txid}
}

func predecessor(b byte) base.OutputID {
	var txid base.TransactionID
	txid[0] = b
	return base.MustNewOutputID(txid, 0)
}

// slot first, then the smaller VRF output, then the txid; each key only decides
// when the previous ones are equal
func TestMineTransitOrder(t *testing.T) {
	pred := predecessor(1)
	older := transitOf(pred, 10, []byte{0xff}, 1)
	newer := transitOf(pred, 11, []byte{0x00}, 2)
	require.True(t, older.BetterThan(newer), "the older slot wins whatever the VRF output")
	require.False(t, newer.BetterThan(older))

	small := transitOf(pred, 10, []byte{0x01, 0x00}, 9)
	big := transitOf(pred, 10, []byte{0x01, 0x01}, 2)
	require.True(t, small.BetterThan(big), "equal slots break on the smaller VRF output, not the txid")
	require.False(t, big.BetterThan(small))

	a := transitOf(pred, 10, []byte{0x01}, 2)
	b := transitOf(pred, 10, []byte{0x01}, 9)
	require.True(t, a.BetterThan(b), "equal VRF outputs break on the lower txid")
	require.False(t, b.BetterThan(a))
}
