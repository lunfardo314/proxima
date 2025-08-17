package txbuilder_seq

import (
	"crypto/ed25519"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

// initializes ledger.Library singleton for all tests and creates testing genesis private key

var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerIDData()
}

func TestBase(t *testing.T) {
	privKey := testutil.GetTestingPrivateKey()
	addr := ledger.AddressED25519FromPrivateKey(privKey)
	seqID := base.RandomChainID()
	bal := ledger.L().ID.MinimumAmountOnSequencer << 8

	sd := seqdata.New().
		SetName("test_seq").
		IncBranchHeight(2).
		IncChainHeight(4)

	predChain := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(bal)
		o.WithLock(addr)
		ccIdx := o.MustPushConstraint(ledger.NewChainConstraint(seqID, 0, 2, 1000, bal).Bytes())
		_ = o.MustPushConstraint(ledger.NewSequencerConstraint(ccIdx).Bytes())
		_ = o.MustPushConstraint(easyfl.InlineDataBytecode(sd.Bytes()))
	})
	predTs := base.NewLedgerTime(1000, 50)
	predID := base.MustNewOutputID(base.RandomTransactionID(true, 2, predTs), 0)

	pred, ok := ledger.AsOutputWithChainID(predChain, predID)
	require.True(t, ok)
	t.Logf("\n--------- predecessor --------\n%s", pred.String())

	ts := predTs.AddSlots(1)
	txb, err := New(ts, &pred, nil, privKey)
	require.NoError(t, err)
	rndEndorsement := base.RandomTransactionID(true, 2, base.NewLedgerTime(ts.Slot, 0))
	err = txb.AddEndorsement(rndEndorsement)
	require.NoError(t, err)

	_, _, txString, err := txb.BytesWithValidation()
	t.Logf("\n--------- tx --------\n%s", txString)

	require.NoError(t, err)

}
