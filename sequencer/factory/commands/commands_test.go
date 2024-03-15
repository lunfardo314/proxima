package commands

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func init() {
	ledger.InitWithTestingLedgerIDData(
		ledger.WithTickDuration(10*time.Millisecond),
		ledger.WithTransactionPace(1),
		ledger.WithSequencerPace(1))
}

func TestBase(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		addrController := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1000))
		addrTarget := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2000))
		o, err := MakeSequencerWithdrawCmdOutput(MakeSequencerWithdrawCmdOutputParams{
			SeqID:          ledger.RandomChainID(),
			ControllerAddr: addrController,
			TargetLock:     addrTarget,
			TagAlongFee:    500,
			Amount:         1_000_000,
		})
		require.NoError(t, err)
		t.Logf("commnd output:\n%s", o.ToString("    "))

		parser := NewCommandParser(addrController)
		sendOutput, err := parser.ParseSequencerCommandToOutput(&ledger.OutputWithID{Output: o})
		require.NoError(t, err)
		t.Logf("send output:\n%s", sendOutput[0].ToString("    "))
	})
	t.Run("not ok", func(t *testing.T) {
		addrController := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1000))
		addrTarget := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2000))
		_, err := MakeSequencerWithdrawCmdOutput(MakeSequencerWithdrawCmdOutputParams{
			SeqID:          ledger.RandomChainID(),
			ControllerAddr: addrController,
			TargetLock:     addrTarget,
			TagAlongFee:    500,
			Amount:         1_000,
		})
		common.RequireErrorWith(t, err, "is less than required minimum")
	})
}
