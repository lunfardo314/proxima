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
func (e *testEnv) LatestBranchSlot() uint32                                { return 0 }
func (e *testEnv) BranchDataForSlot(uint32) []*multistate.BranchData       { return nil }
func (e *testEnv) TxBytesStore() global.TxBytesStore                       { return nil }
func (e *testEnv) GetConnectivityMatrix() *api.ConnectivityMatrix          { return nil }
func (e *testEnv) SubscribeMiningTx(func(base.TransactionID, []byte) bool) {}

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
	require.True(t, live.FairLaunch.Present)
	// nothing seen on the mining stream, so the contest block stays absent
	require.Nil(t, live.FairLaunch.Contest)
	require.EqualValues(t, 0, live.FairLaunch.MinedTransactions)
	require.EqualValues(t, 0, live.FairLaunch.MinedAmount)
	require.Equal(t, live.FairLaunch.Ceiling, live.InitialSupply+live.FairLaunch.Remaining,
		"ceiling T must equal I + R at genesis, when nothing is mined yet")
	require.EqualValues(t, ledger.L(base.MaxSlot).Constants.MineAmount, live.FairLaunch.Amount)

	// the annual inflation cap: an upper bound, so it must dominate what a year
	// of branch bonuses alone could pay, and stay a fraction of the supply
	lib := ledger.L(base.MaxSlot)
	slotsPerYear := uint64(lib.SlotsPerYear())
	require.EqualValues(t, lib.BranchInflationBonusBase(live.CurrentSlot)*slotsPerYear, live.AnnualBranchBonusCap)
	require.Greater(t, live.AnnualChainInflationCap, uint64(0))
	require.Greater(t, live.AnnualInflationCapRate, 0.0)
	require.Less(t, live.AnnualInflationCapRate, 1.0)
	t.Logf("annual inflation cap: %.2f%% of supply (chain %d + branch bonus %d over %d slots)",
		100*live.AnnualInflationCapRate, live.AnnualChainInflationCap, live.AnnualBranchBonusCap, slotsPerYear)

	// the identity block the page header renders: the clock must be internally
	// consistent, since the page derives the current slot from it locally
	lc := live.Ledger
	require.EqualValues(t, ledger.L(0).GenesisTime().Unix(), lc.GenesisTimeUnix)
	require.EqualValues(t, float64(lc.TicksPerSlot)*lc.TickDurationMs, float64(lc.SlotDurationMs))
	require.EqualValues(t, base.PROX, lc.MotesPerToken)
	require.Equal(t, base.BaseTokenName, lc.TokenName)
	require.Len(t, lc.LibraryHash, 64)
	t.Logf("ledger: slot 0 at %s, slot = %d ticks x %.0f ms = %d ms, library %s since slot %d",
		time.Unix(lc.GenesisTimeUnix, 0).Format(time.DateTime),
		lc.TicksPerSlot, lc.TickDurationMs, lc.SlotDurationMs, lc.LibraryHash[:12], lc.LibraryUpgradeSlot)

	// the whole response must serialize — this is what the page fetches
	b, err := json.MarshalIndent(&response{Live: live}, "", "  ")
	require.NoError(t, err)
	require.Contains(t, string(b), "\"fair_launch\"")
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

// TestContestWindow checks the mining-stream window: only transits whose proof
// of work was verified count as competing miners, unverified arrivals are
// reported separately rather than inflating the figure, transits racing the
// same predecessor are counted as a contest, and observations fall out of the
// window as the LRB slot advances past them.
func TestContestWindow(t *testing.T) {
	m := &Monitor{}
	predA := base.MustNewOutputID(base.TransactionID{1}, 0)
	predB := base.MustNewOutputID(base.TransactionID{2}, 0)
	now := time.Now()

	m.observed = []mineObservation{
		// two miners racing the same predecessor: a real contest
		{slot: 100, miner: "aa", difficulty: 22, predecessor: predA, verified: true, when: now},
		{slot: 100, miner: "bb", difficulty: 22, predecessor: predA, verified: true, when: now},
		// a third miner on the next predecessor, and the newest observation, so
		// it is the one the live difficulty reading comes from
		{slot: 104, miner: "cc", difficulty: 23, predecessor: predB, verified: true, when: now.Add(time.Second)},
		// a forgery: claims the shape, fails the work check
		{slot: 104, miner: "zz", difficulty: 40, predecessor: predB, verified: false, when: now},
	}

	c := m.contest(110)
	require.NotNil(t, c)
	require.Equal(t, 3, c.CompetingMiners, "the unverified arrival must not count as a miner")
	require.Equal(t, 3, c.Submissions)
	require.Equal(t, 1, c.Rejected)
	require.Equal(t, 2, c.MaxRacingSamePredecessor, "two transits raced predecessor A")
	require.EqualValues(t, 23, c.Difficulty, "difficulty comes from the newest verified transit")

	// the window drops observations older than contestWindowSlots and, once
	// empty, reports nothing at all rather than zeroes
	require.NotNil(t, m.contest(100+contestWindowSlots))
	require.Nil(t, m.contest(200+contestWindowSlots))
	require.Empty(t, m.observed)
}
