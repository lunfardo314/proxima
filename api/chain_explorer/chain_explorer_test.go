package chain_explorer

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// initializes the ledger singleton with testing data, mirroring the utxodb
// package test bootstrap.
var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerData(
		ledger.WithBranchCoverageBounds(0, 2*ledger.DefaultInitialSupply),
	)
}

// chainIDsControllerFullScan collects, by walking EVERY chain tip
// (IterateChainedOutputs) and filtering in-memory, the set of chainIDs whose
// controller (index_values[0]) equals the given hex value. This is the
// pre-optimization reference behaviour.
func chainIDsControllerFullScan(t *testing.T, rdr multistate.SugaredStateReader, lib *ledger.Library, controllerHex string) map[string]struct{} {
	got := make(map[string]struct{})
	err := rdr.IterateChainedOutputs(func(o ledger.OutputWithChainID) bool {
		rw := makeRow(&o, lib, 0)
		if len(rw.IndexValues) > 0 && rw.IndexValues[0] == controllerHex {
			got[rw.ChainID] = struct{}{}
		}
		return true
	})
	require.NoError(t, err)
	return got
}

// chainIDsControllerIndexedScan collects the same set via the new indexed path:
// prefix-scan the controllers partition for the controller value, wrap each
// candidate with asChainOutput (drops non-chain outputs), and keep those whose
// index_values[0] matches.
func chainIDsControllerIndexedScan(t *testing.T, rdr multistate.SugaredStateReader, lib *ledger.Library, controllerHex string) map[string]struct{} {
	raw, err := hex.DecodeString(controllerHex)
	require.NoError(t, err)
	got := make(map[string]struct{})
	err = rdr.IterateOutputsForAccount(raw, func(oid base.OutputID, o *ledger.Output) bool {
		owc, ok := asChainOutput(o, oid)
		if !ok {
			return true
		}
		rw := makeRow(owc, lib, 0)
		if len(rw.IndexValues) > 0 && rw.IndexValues[0] == controllerHex {
			got[rw.ChainID] = struct{}{}
		}
		return true
	})
	require.NoError(t, err)
	return got
}

// TestControllerIndexedScanEquivalence builds a small ledger with several chains
// controlled by two different addresses (plus plain non-chain outputs in the
// same accounts) and asserts the indexed controller scan returns exactly the
// same chain set as the full chain walk. This is the core correctness claim of
// the indexed-filter optimization in serveList.
func TestControllerIndexedScanEquivalence(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	lib := ledger.L(base.MaxSlot)

	// Two controllers, each funded from the faucet so they also hold plain
	// (non-chain) sigLock outputs — those must NOT leak into the chain set even
	// though the indexed scan returns them as candidates.
	privA, _, addrA := u.GenerateAddress(1)
	privB, _, addrB := u.GenerateAddress(2)
	require.NoError(t, u.TokensFromFaucet(addrA, 1_000_000_000))
	require.NoError(t, u.TokensFromFaucet(addrB, 1_000_000_000))

	// Derive chain-origin timestamps from real output timestamps (never
	// ledger.TimeNow) to avoid wall-clock vs ledger-time races.
	outsA, err := u.StateReader().GetUTXOsForController(addrA.ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, outsA)
	tsA := outsA[0].ID.Timestamp().AddSlots(1)

	outsB, err := u.StateReader().GetUTXOsForController(addrB.ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, outsB)
	tsB := outsB[0].ID.Timestamp().AddSlots(1)

	// Two chains for A, one for B.
	chA1, err := u.MakeNewChain(100_000_000, privA, addrA, tsA)
	require.NoError(t, err)
	chA2, err := u.MakeNewChain(100_000_000, privA, addrA, tsA)
	require.NoError(t, err)
	chB1, err := u.MakeNewChain(100_000_000, privB, addrB, tsB)
	require.NoError(t, err)

	rdr := u.SugaredStateReader()
	ctrlA := hex.EncodeToString(addrA.ControllerID())
	ctrlB := hex.EncodeToString(addrB.ControllerID())

	fullA := chainIDsControllerFullScan(t, rdr, lib, ctrlA)
	idxA := chainIDsControllerIndexedScan(t, rdr, lib, ctrlA)
	require.Equal(t, fullA, idxA, "controller A: indexed scan must equal full scan")
	require.Contains(t, fullA, chA1.ChainID.StringHex())
	require.Contains(t, fullA, chA2.ChainID.StringHex())
	require.NotContains(t, fullA, chB1.ChainID.StringHex())

	fullB := chainIDsControllerFullScan(t, rdr, lib, ctrlB)
	idxB := chainIDsControllerIndexedScan(t, rdr, lib, ctrlB)
	require.Equal(t, fullB, idxB, "controller B: indexed scan must equal full scan")
	require.Contains(t, fullB, chB1.ChainID.StringHex())
	require.NotContains(t, fullB, chA1.ChainID.StringHex())
}
