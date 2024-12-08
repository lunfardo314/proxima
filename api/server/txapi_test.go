package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/tests"
	"github.com/lunfardo314/proxima/util/utxodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//var genesisPrivateKey ed25519.PrivateKey

func init() {
	// genesisPrivateKey = ledger.InitWithTestingLedgerIDData()
}

func TestCompileScript(t *testing.T) {
	server := &server{}

	// Prepare request
	req := httptest.NewRequest(http.MethodGet, "/txapi/v1/compile_script?source=slice(0x0102,0,0)", nil)
	w := httptest.NewRecorder()

	// Call handler
	server.compileScript(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.Bytecode
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)
	assert.Equal(t, "1182010281008100", ret.Bytecode) // Hex for "compiledBytecode"

}

func TestDecompileBytecode(t *testing.T) {
	server := &server{}

	// Prepare request
	req := httptest.NewRequest(http.MethodGet, "/txapi/v1/decompile_bytecode?bytecode=1182010281008100", nil)
	w := httptest.NewRecorder()

	// Call handler
	server.decompileBytecode(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.ScriptSource
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)
	assert.Equal(t, "slice(0x0102,0,0)", ret.Source) // Hex for "compiledBytecode"
}

func TestParseOutputData(t *testing.T) {
	server := &server{}

	// Prepare request
	req := httptest.NewRequest(http.MethodGet, "/txapi/v1/parse_output_data?output_data=40060b45ab8800038d7ff693a50e2345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7ff693a50e1d504287626f6f742e623084000006ad840000035c8800000000000000006151d7880000000000386580d1022cee903827166af9c0257be156222ae34c9279831a294fb5213647a3bcbe7a3e203d100f3ec2095fb076c65ed1d29a680c05c7993d43e7fdd0a779e8f783943a50b0a1ba3b373830cc2f447a030edb35810281ff", nil)
	w := httptest.NewRecorder()

	// Call handler
	server.parseOutputData(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.ParsedOutput
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)

	assert.Equal(t, ret.Data, "40060b45ab8800038d7ff693a50e2345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7ff693a50e1d504287626f6f742e623084000006ad840000035c8800000000000000006151d7880000000000386580d1022cee903827166af9c0257be156222ae34c9279831a294fb5213647a3bcbe7a3e203d100f3ec2095fb076c65ed1d29a680c05c7993d43e7fdd0a779e8f783943a50b0a1ba3b373830cc2f447a030edb35810281ff")
	assert.Equal(t, ret.Amount, uint64(0x38d7ff693a50e))
	assert.Equal(t, ret.ChainID, "6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc")
	assert.Equal(t, len(ret.Constraints), 6)
	assert.Equal(t, ret.Constraints[0], "amount(u64/1000005667366158)")
}

func TestParseOutput(t *testing.T) {
	server := &server{}

	//genesisPrivateKey := testutil.GetTestingPrivateKey()
	// genesisPrivateKey := ledger.InitWithTestingLedgerIDData(
	// 	ledger.WithTickDuration(4*time.Millisecond),
	// 	ledger.WithTransactionPace(3),
	// 	ledger.WithSequencerPace(3))

	u := utxodb.NewUTXODB(tests.TestGenesisPrivKey(), true)

	_, _, addrs := u.GenerateAddressesWithFaucetAmount(0, 1, 100_000_000)
	odatas, err := u.StateReader().GetUTXOsLockedInAccount(addrs[0].AccountID())
	assert.NoError(t, err)

	// Prepare request
	request := fmt.Sprintf("/txapi/v1/parse_output?output_id=%s", odatas[0].ID.StringHex())
	req := httptest.NewRequest(http.MethodGet, request, nil)
	w := httptest.NewRecorder()

	// Call handler
	server.parseOutput(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.ParsedOutput
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)

	// TODO
	// assert.Equal(t, ret.Data, "40060b45ab8800038d7ff693a50e2345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7ff693a50e1d504287626f6f742e623084000006ad840000035c8800000000000000006151d7880000000000386580d1022cee903827166af9c0257be156222ae34c9279831a294fb5213647a3bcbe7a3e203d100f3ec2095fb076c65ed1d29a680c05c7993d43e7fdd0a779e8f783943a50b0a1ba3b373830cc2f447a030edb35810281ff")
	// assert.Equal(t, ret.Amount, uint64(0x38d7ff693a50e))
	// assert.Equal(t, ret.ChainID, "6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc")
	// assert.Equal(t, len(ret.Constraints), 6)
	// assert.Equal(t, ret.Constraints[0], "amount(u64/1000005667366158)")
}

func TestGetTXBytes(t *testing.T) {

	env, txid, err := tests.InitTestEnv()
	require.NoError(t, err)

	// Mock server
	mockServer := &server{
		environment: env,
	}

	// Prepare request
	request := fmt.Sprintf("/txapi/v1/get_txbytes?txid=%s", txid.StringHex())
	req := httptest.NewRequest(http.MethodGet, request, nil)
	w := httptest.NewRecorder()

	// Call handler
	mockServer.getTxBytes(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.TxBytes
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)
	//assert.Equal(ret.TxBytes, txBytes

}

func TestGetParsedTransaction(t *testing.T) {
	//privKey := genesisPrivateKey
	env, txid, err := tests.InitTestEnv()
	require.NoError(t, err)

	// Mock server
	mockServer := &server{
		environment: env,
	}

	// Prepare request
	request := fmt.Sprintf("/txapi/v1/get_parsed_transaction?txid=%s", txid.StringHex())
	req := httptest.NewRequest(http.MethodGet, request, nil)
	w := httptest.NewRecorder()

	// Call handler
	mockServer.getParsedTransaction(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.TransactionJSONAble
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)
	assert.Equal(t, ret.TotalAmount, uint64(0x38d7ea4c68000))
	assert.Equal(t, ret.IsBranch, true)
	assert.Equal(t, len(ret.Inputs), 2)
	assert.Equal(t, len(ret.Outputs), 5)
}

func TestGetVertexDep(t *testing.T) {

	env, txid, err := tests.InitTestEnv()
	require.NoError(t, err)

	// Mock server
	mockServer := &server{
		environment: env,
	}

	// Prepare request
	request := fmt.Sprintf("/txapi/v1/get_vertex_dep?txid=%s", txid.StringHex())
	req := httptest.NewRequest(http.MethodGet, request, nil)
	w := httptest.NewRecorder()

	// Call handler
	mockServer.getVertexWithDependencies(w, req)

	// Validate response
	resp := w.Result()
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := io.ReadAll(resp.Body)
	assert.NoError(t, err)

	var ret api.VertexWithDependencies
	err = json.Unmarshal(data, &ret)
	assert.NoError(t, err)
	// assert.Equal(t, ret.TotalAmount, uint64(0x38d7ea4c68000))
	// assert.Equal(t, ret.IsBranch, true)
	// assert.Equal(t, len(ret.Inputs), 2)
	// assert.Equal(t, len(ret.Outputs), 5)
}

// use this function is avoid crash for err = nil
func (srv *server) AssertNoError(err error, prefix ...string) {
	if err != nil {
		pref := "error: "
		if len(prefix) > 0 {
			pref = strings.Join(prefix, " ") + ": "
		}
		log.Fatalf(pref+"%v", err)
	}
}

// type workflowDummyEnvironment struct {
// 	*global.Global
// 	stateStore   global.StateStore
// 	txBytesStore global.TxBytesStore
// 	chainId      ledger.ChainID
// 	root         common.VCommitment
// }

// func (wrk *workflowDummyEnvironment) GetKnownLatestMilestonesJSONAble() map[string]tippool.LatestSequencerTipDataJSONAble {
// 	return nil
// }

// func (p *workflowDummyEnvironment) GetLatestReliableBranch() (ret *multistate.BranchData) {
// 	return nil
// }

// func (p *workflowDummyEnvironment) GetNodeInfo() *global.NodeInfo {
// 	return nil
// }

// func (p *workflowDummyEnvironment) GetPeersInfo() *api.PeersInfo {
// 	return nil
// }

// func (p *workflowDummyEnvironment) GetSyncInfo() *api.SyncInfo {
// 	return nil
// }

// func (p *workflowDummyEnvironment) GetTxInclusion(txid *ledger.TransactionID, slotsBack int) *multistate.TxInclusion {
// 	return nil
// }

// func (w *workflowDummyEnvironment) StateStore() global.StateStore {
// 	return w.stateStore
// }

// func (w *workflowDummyEnvironment) TxBytesStore() global.TxBytesStore {
// 	return w.txBytesStore
// }

// func (w *workflowDummyEnvironment) GetOwnSequencerID() *ledger.ChainID {
// 	panic("not implemented")
// }

// func (w *workflowDummyEnvironment) SnapshotBranchID() *ledger.TransactionID {
// 	return ledger.GenesisTransactionID()
// }

// func (w *workflowDummyEnvironment) DurationSinceLastMessageFromPeer() time.Duration {
// 	return 0
// }

// func (w *workflowDummyEnvironment) SelfPeerID() peer.ID {
// 	return "self"
// }

// func (w *workflowDummyEnvironment) EvidencePastConeSize(_ int) {}

// func (w *workflowDummyEnvironment) EvidenceNumberOfTxDependencies(_ int) {}

// func (w *workflowDummyEnvironment) PullFromNPeers(nPeers int, txid *ledger.TransactionID) int {
// 	w.Log().Warnf(">>>>>> PullFromNPeers not implemented: %s", txid.StringShort())
// 	return 0
// }

// func (p *workflowDummyEnvironment) LatestReliableState() (multistate.SugaredStateReader, error) {
// 	return multistate.MakeSugared(multistate.MustNewReadable(p.stateStore, p.root, 0)), nil
// }

// func (p *workflowDummyEnvironment) QueryTxIDStatusJSONAble(txid *ledger.TransactionID) vertex.TxIDStatusJSONAble {
// 	return vertex.TxIDStatusJSONAble{}
// }

// func (p *workflowDummyEnvironment) SubmitTxBytesFromAPI(txBytes []byte) {
// }

// func newWorkflowDummyEnvironment(stateStore global.StateStore, txStore global.TxBytesStore) *workflowDummyEnvironment {
// 	return &workflowDummyEnvironment{
// 		Global:       global.NewDefault(false),
// 		stateStore:   stateStore,
// 		txBytesStore: txStore,
// 	}
// }

// func InitTestEnv() (*workflowDummyEnvironment, ledger.TransactionID, error) {
// 	privKey := genesisPrivateKey
// 	ledger.DefaultIdentityData(privKey)
// 	addr1 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
// 	addr2 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
// 	distrib := []ledger.LockBalance{
// 		{Lock: addr1, Balance: 1_000_000, ChainOrigin: false},
// 		{Lock: addr2, Balance: 1_000_000, ChainOrigin: false},
// 		{Lock: addr2, Balance: 1_000_000, ChainOrigin: true},
// 	}

// 	stateStore := common.NewInMemoryKVStore()
// 	multistate.InitStateStore(*ledger.L().ID, stateStore)
// 	txBytesStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
// 	env := newWorkflowDummyEnvironment(stateStore, txBytesStore)

// 	env.StartTracingTags(global.TraceTag)

// 	workflow.Start(env, peering.NewPeersDummy(), workflow.OptionDoNotStartPruner)

// 	txBytes, err := txbuilder.DistributeInitialSupply(stateStore, privKey, distrib)
// 	//assert.NoError(t, err)
// 	txid, err := txBytesStore.PersistTxBytesWithMetadata(txBytes, nil)
// 	//require.NoError(t, err)

// 	return env, txid, err
// }
