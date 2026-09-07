package tests

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// The input queue asks IsMiningTransaction on transactions that passed only
// stage-1 parse, i.e. on raw bytes from a peer. A transaction whose top-level
// tuple is well-formed but whose produced output 0 is junk used to trip an
// assertion inside the accessor and kill the node. The shape must simply be
// classified as "not a mining transaction".
func TestMiningShapeOnMalformedOutput(t *testing.T) {
	ts := base.LedgerTime{Slot: 1, Tick: 1}
	oid := base.OutputID{}
	d := &txbuildercore.TxRawData{
		UpgradeIndex:         ledger.L(ts.Slot).UpgradeIndex(),
		Timestamp:            ts,
		SequencerOutputIndex: txbuildercore.SequencerOutputIndexNone,
		InputIDs:             []*base.OutputID{&oid},
		UnlockBlocks:         []*txbuildercore.UnlockParams{txbuildercore.NewUnlockBlock()},
		// 1 input and 3 outputs is the mining shape; output 0 is not an output tuple at all
		OutputBytes: [][]byte{{0xff, 0x00, 0x01}, {0x01}, {0x02}},
	}
	tx, err := transaction.Parse(txbuildercore.SerializeRawTxBytes(d))
	require.NoError(t, err)
	require.False(t, tx.IsMiningTransaction())

}
