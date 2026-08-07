package monitor

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerData(
		ledger.WithCoverageContributionBounds(0, 2*ledger.DefaultInitialSupply),
	)
}

// testEnv is the minimal Env over a utxodb in-memory state. The branch-data and
// txstore members are the parts a real node supplies; the census and the live
// chain walk need neither, so they are stubbed out.
type testEnv struct {
	global.Logging
	u *utxodb.UTXODB
}

func (e *testEnv) Ctx() context.Context { return context.Background() }
func (e *testEnv) LatestReliableState() (multistate.SugaredStateReader, error) {
	return e.u.SugaredStateReader(), nil
}
func (e *testEnv) GetLatestReliableBranch() *multistate.BranchData {
	// utxodb has no branches. Supply the aggregates the monitor reads off one,
	// with a stem carrying a valid output ID — Slot()/TxID() go through it.
	return &multistate.BranchData{
		Supply: e.u.Supply(),
		Stem:   &ledger.OutputWithID{ID: base.MustNewOutputID(base.TransactionID{}, 0)},
	}
}
func (e *testEnv) LatestBranchSlot() uint32                          { return 0 }
func (e *testEnv) BranchDataForSlot(uint32) []*multistate.BranchData { return nil }
func (e *testEnv) TxBytesStore() global.TxBytesStore                 { return nil }
func (e *testEnv) GetConnectivityMatrix() *api.ConnectivityMatrix    { return nil }

// TestCensusAccounting builds a small state with several controllers holding
// both plain and chained outputs, then asserts the census counts accounts as
// distinct controllers (not outputs), splits balances by class, and conserves
// the total. This is the core claim of the periodic tier.
func TestCensusAccounting(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)

	// three funded controllers; one of them also owns two chains
	privA, _, addrA := u.GenerateAddress(1)
	_, _, addrB := u.GenerateAddress(2)
	_, _, addrC := u.GenerateAddress(3)
	require.NoError(t, u.TokensFromFaucet(addrA, 2_000_000_000))
	require.NoError(t, u.TokensFromFaucet(addrB, 1_000_000_000))
	require.NoError(t, u.TokensFromFaucet(addrC, 500_000_000))
	// a second faucet payment to A: two outputs, still one account
	require.NoError(t, u.TokensFromFaucet(addrA, 300_000_000))

	outsA, err := u.StateReader().GetUTXOsForController(addrA.ControllerID())
	require.NoError(t, err)
	require.NotEmpty(t, outsA)
	tsA := outsA[0].ID.Timestamp().AddSlots(1)
	_, err = u.MakeNewChain(100_000_000, privA, addrA, tsA)
	require.NoError(t, err)
	_, err = u.MakeNewChain(150_000_000, privA, addrA, tsA)
	require.NoError(t, err)

	m := &Monitor{env: &testEnv{u: u}}
	census, err := m.collectCensus()
	require.NoError(t, err)

	// conservation: the scanned balance must equal the ledger supply
	require.EqualValues(t, u.Supply(), census.TotalBalance)

	// class balances must sum back to the scanned total, and outputs to NumUTXOs
	var sumBalance uint64
	var sumOutputs int
	for _, c := range census.Classes {
		sumBalance += c.Balance
		sumOutputs += c.NumOutputs
	}
	require.EqualValues(t, census.TotalBalance, sumBalance)
	require.Equal(t, census.NumUTXOs, sumOutputs)

	// on-chain + plain split is exhaustive
	require.EqualValues(t, census.TotalBalance, census.OnChainBalance+census.PlainLockBalance)

	// A holds several outputs but is one account, and its balance is the sum of
	// everything indexed under it — plain outputs and both chains
	ctrlA := hex.EncodeToString(addrA.ControllerID())
	var rowA *accountRow
	for i := range census.TopChain {
		if census.TopChain[i].Controller == ctrlA {
			rowA = &census.TopChain[i]
		}
	}
	require.NotNil(t, rowA, "controller A must appear among the chained accounts")
	require.Greater(t, rowA.NumOutputs, 2, "A's outputs are aggregated into one account row")

	// A must not also show up as a plain account: a controller lands in exactly
	// one of the two lists
	for _, r := range census.TopPlain {
		require.NotEqual(t, ctrlA, r.Controller)
	}

	// B and C hold only plain outputs
	require.Contains(t, controllers(census.TopPlain), hex.EncodeToString(addrB.ControllerID()))
	require.Contains(t, controllers(census.TopPlain), hex.EncodeToString(addrC.ControllerID()))

	// the mine chain is present in genesis and gets its own class
	require.Contains(t, classNames(census), classMine)

	t.Logf("census: %d UTXOs, %d accounts, %d chains, pass %d ms",
		census.NumUTXOs, census.NumControllers, census.NumChains, census.AsOf.DurationMs)
	for _, c := range census.Classes {
		t.Logf("   %-20s outputs=%-4d accounts=%-4d balance=%d", c.Class, c.NumOutputs, c.NumControllers, c.Balance)
	}
}

// TestLiveSection checks the live tier over the same synthetic state: the mine
// chain tip is picked up with its genesis R, and the JSON the page consumes
// round-trips.
func TestLiveSection(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	m := &Monitor{env: &testEnv{u: u}}

	live, err := m.collectLive()
	require.NoError(t, err)
	require.EqualValues(t, u.Supply(), live.Supply)

	// mine chain: present at genesis, nothing mined yet, R at R_init
	require.True(t, live.Mining.Present)
	require.EqualValues(t, 0, live.Mining.MinedTransactions)
	require.EqualValues(t, 0, live.Mining.MinedAmount)
	require.Equal(t, live.Mining.Ceiling, live.InitialSupply+live.Mining.Remaining,
		"ceiling T must equal I + R at genesis, when nothing is mined yet")
	require.EqualValues(t, ledger.L(base.MaxSlot).Constants.MineAmount, live.Mining.Amount)

	// the whole response must serialize — this is what the page fetches
	b, err := json.MarshalIndent(&response{Live: live}, "", "  ")
	require.NoError(t, err)
	require.Contains(t, string(b), "\"mining\"")
	t.Logf("live section collected in %d ms", live.AsOf.DurationMs)
}

// TestCensusScaling grows the state and reports the per-UTXO cost of the census
// pass, which is the measurement spec 0 leaves open (TBD-p 1 and 2). It asserts
// only that the pass stays correct as the state grows; the timings are logged
// for the spec, not asserted on.
func TestCensusScaling(t *testing.T) {
	u := utxodb.NewUTXODB(genesisPrivateKey, true)
	m := &Monitor{env: &testEnv{u: u}}

	const accounts = 300
	for i := 0; i < accounts; i++ {
		_, _, addr := u.GenerateAddress(i + 1)
		require.NoError(t, u.TokensFromFaucet(addr, 10_000_000))
	}

	start := time.Now()
	census, err := m.collectCensus()
	require.NoError(t, err)
	elapsed := time.Since(start)

	require.EqualValues(t, u.Supply(), census.TotalBalance)
	require.GreaterOrEqual(t, census.NumControllers, accounts)

	perUTXO := float64(elapsed.Nanoseconds()) / float64(census.NumUTXOs)
	fmt.Printf("CENSUS COST: %d UTXOs, %d accounts, %v total, %.0f ns/UTXO\n",
		census.NumUTXOs, census.NumControllers, elapsed, perUTXO)
	t.Logf("extrapolated to 1M UTXOs: %.1f s", perUTXO*1e6/1e9)
}

func controllers(rows []accountRow) []string {
	ret := make([]string, 0, len(rows))
	for _, r := range rows {
		ret = append(ret, r.Controller)
	}
	return ret
}

func classNames(c *censusSection) []string {
	ret := make([]string, 0, len(c.Classes))
	for _, r := range c.Classes {
		ret = append(ret, r.Class)
	}
	return ret
}
