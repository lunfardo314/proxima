package tests

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/stretchr/testify/require"
)

// submitPolicyFixturePath is the fixture read by the api/server test of the validate_only path.
const submitPolicyFixturePath = "../../api/server/testdata/submit_policy_fixture.json.gz"

// TestGenerateSubmitPolicyFixture writes the fixture of api/server's TestSubmitTxRunsNodePolicy:
// the non-canonical on-hold delegation transaction, which the ledger accepts and the node's
// policy checks refuse, with its consumed outputs and the test libraries it validates against, gzipped.
// It runs only with PROXIMA_WRITE_FIXTURE set; regenerate whenever the test libraries change.
func TestGenerateSubmitPolicyFixture(t *testing.T) {
	if os.Getenv("PROXIMA_WRITE_FIXTURE") == "" {
		t.Skip("set PROXIMA_WRITE_FIXTURE to regenerate " + submitPolicyFixturePath)
	}
	saved, savedBound := transaction.DelegationStateCheckFromSlot, transaction.FrozenCoverageBoundFromSlot
	transaction.DelegationStateCheckFromSlot, transaction.FrozenCoverageBoundFromSlot = 0, 0
	defer func() {
		transaction.DelegationStateCheckFromSlot, transaction.FrozenCoverageBoundFromSlot = saved, savedBound
	}()

	td := &testData{T: t, seqBalance: 20_000_000_000}
	td.init()
	withUpgrade6(t, td.u)
	at := uint32(upgrade6Slot + 10)
	_, _, err := td.initDelegationUTXOMake(base.T(at, 10), 4, 0)
	require.NoError(t, err)
	ts := base.T(at+1, 10)
	require.NoError(t, td.transitChainWithDelegationWithMake(1, transitWithMakeParams{
		ts: ts, freezeUntilEpoch: td.delegatedOutput.FreezeUntilMax(ts), inflate: true, disableConsistencyChecks: true,
	}))
	tx, _, err := td.holdWithState(base.T(at+2, 10), compileState(t, "delegateLockState(0x0000000000, 2, 0x0000)"))
	require.NoError(t, err)
	require.NoError(t, tx.ValidateFullContext(), "the ledger accepts the transaction")
	require.Error(t, tx.CheckNodePolicy(), "the node's policy refuses it")

	inputs := make([]string, tx.NumInputs())
	ids := make([]string, tx.NumInputs())
	for i := range inputs {
		o, err := tx.ConsumedOutputAt(byte(i))
		require.NoError(t, err)
		inputs[i] = hex.EncodeToString(o.Output.Bytes())
		ids[i] = o.ID.StringHex()
	}
	// every library of the chain: the transaction version is the position of its library in it
	libs := map[uint32]json.RawMessage{0: ledger.L(0).DefinitionsJSON()}
	for _, u := range ledger.Upgrades {
		libs[u.Slot] = ledger.L(u.Slot).DefinitionsJSON()
	}
	blob, err := json.Marshal(map[string]any{
		"tx_bytes": hex.EncodeToString(tx.Bytes()), "consumed_utxos": inputs, "input_ids": ids, "libraries": libs,
	})
	require.NoError(t, err)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err = zw.Write(blob)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, os.WriteFile(submitPolicyFixturePath, buf.Bytes(), 0644))
}
