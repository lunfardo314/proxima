package noworkflow

import (
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	transaction2 "github.com/lunfardo314/proxima/ledger/transaction"
	txbuilder2 "github.com/lunfardo314/proxima/ledger/txbuilder"
	state "github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/lunfardo314/proxima/utangle_old"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/proxima/util/testutil/inittest"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

func TestOriginTangle(t *testing.T) {
	t.Run("origin", func(t *testing.T) {
		par := genesis.DefaultIdentityData(testutil.GetTestingPrivateKey())
		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, root := genesis.InitLedgerState(*par, stateStore)
		ut := utangle_old.Load(stateStore)
		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())
		t.Logf("genesis root: %s", root.String())
		t.Logf("%s", ut.Info(true))
	})
	t.Run("origin with distribution", func(t *testing.T) {
		privKey := testutil.GetTestingPrivateKey()
		par := genesis.DefaultIdentityData(privKey)
		addr1 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(1))
		addr2 := ledger.AddressED25519FromPrivateKey(testutil.GetTestingPrivateKey(2))
		distrib := []ledger.LockBalance{
			{Lock: addr1, Balance: 1_000_000},
			{Lock: addr2, Balance: 2_000_000},
		}
		stateStore := common.NewInMemoryKVStore()
		bootstrapChainID, _ := genesis.InitLedgerState(*par, stateStore)
		txBytes, err := txbuilder2.DistributeInitialSupply(stateStore, privKey, distrib)
		require.NoError(t, err)

		txStore := txstore.NewSimpleTxBytesStore(common.NewInMemoryKVStore())
		_, err = txStore.PersistTxBytesWithMetadata(txBytes)
		require.NoError(t, err)

		ut := utangle_old.Load(stateStore)
		t.Logf("bootstrap chain id: %s", bootstrapChainID.String())

		distribTxID, _, err := transaction2.IDAndTimestampFromTransactionBytes(txBytes)
		require.NoError(t, err)

		t.Logf("genesis branch txid: %s", distribTxID.StringShort())
		t.Logf("%s", ut.Info())

		distribVID, ok := ut.GetVertex(&distribTxID)
		require.True(t, ok)
		t.Logf("forks of distribution transaction:\n%s", distribVID.PastTrackLines().String())

		stemOut := ut.HeaviestStemOutput()
		require.EqualValues(t, int(stemOut.ID.TimeSlot()), int(distribTxID.Slot()))
		require.EqualValues(t, 0, stemOut.Output.Amount())
		stemLock, ok := stemOut.Output.StemLock()
		require.True(t, ok)
		require.EqualValues(t, genesis.DefaultSupply, int(stemLock.Supply))

		rdr := ut.HeaviestStateForLatestTimeSlot()
		bal1, n1 := state.BalanceOnLock(rdr, addr1)
		require.EqualValues(t, 1_000_000, int(bal1))
		require.EqualValues(t, 1, n1)

		bal2, n2 := state.BalanceOnLock(rdr, addr2)
		require.EqualValues(t, 2_000_000, int(bal2))
		require.EqualValues(t, 1, n2)

		balChain, nChain := state.BalanceOnLock(rdr, bootstrapChainID.AsChainLock())
		require.EqualValues(t, 0, balChain)
		require.EqualValues(t, 0, nChain)

		balChain = state.BalanceOnChainOutput(rdr, &bootstrapChainID)
		require.EqualValues(t, genesis.DefaultSupply-1_000_000-2_000_000, int(balChain))
	})
}

type conflictTestRunData struct {
	ut               *utangle_old.UTXOTangle
	bootstrapChainID ledger.ChainID
	privKey          ed25519.PrivateKey
	addr             ledger.AddressED25519
	stateIdentity    genesis.LedgerIdentityData
	originBranchTxid ledger.TransactionID
	forkOutput       *ledger.OutputWithID
	txBytes          [][]byte
	outs             []*ledger.OutputWithID
	total            uint64
	pkController     []ed25519.PrivateKey
}

func initConflictTest(t *testing.T, nConflicts int, verbose bool) *conflictTestRunData {
	const initBalance = 10_000
	genesisPrivKey := testutil.GetTestingPrivateKey()
	par := genesis.DefaultIdentityData(genesisPrivKey)
	distrib, privKeys, addrs := inittest.GenesisParamsWithPreDistributionOld(1, initBalance)
	ret := &conflictTestRunData{
		stateIdentity: *par,
		privKey:       privKeys[0],
		addr:          addrs[0],
	}
	require.True(t, ledger.AddressED25519MatchesPrivateKey(ret.addr, ret.privKey))

	ret.pkController = make([]ed25519.PrivateKey, nConflicts)
	for i := range ret.pkController {
		ret.pkController[i] = ret.privKey
	}

	stateStore := common.NewInMemoryKVStore()
	txStore := txstore.NewDummyTxBytesStore()

	ret.bootstrapChainID, _ = genesis.InitLedgerState(ret.stateIdentity, stateStore)
	txBytes, err := txbuilder2.DistributeInitialSupply(stateStore, genesisPrivKey, distrib)
	require.NoError(t, err)
	_, err = txStore.PersistTxBytesWithMetadata(txBytes)
	require.NoError(t, err)

	ret.ut = utangle_old.Load(stateStore)

	t.Logf("bootstrap chain id: %s", ret.bootstrapChainID.String())
	t.Logf("origing branch txid: %s", ret.originBranchTxid.StringShort())

	for i := range distrib {
		t.Logf("distributed %s -> %s", util.GoTh(distrib[i].Balance), distrib[i].Lock.String())
	}
	t.Logf("%s", ret.ut.Info())

	rdr := ret.ut.HeaviestStateForLatestTimeSlot()
	bal, _ := state.BalanceOnLock(rdr, ret.addr)
	require.EqualValues(t, initBalance, int(bal))

	oDatas, err := rdr.GetUTXOsLockedInAccount(ret.addr.AccountID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))

	ret.forkOutput, err = oDatas[0].Parse()
	require.NoError(t, err)
	require.EqualValues(t, initBalance, int(ret.forkOutput.Output.Amount()))
	t.Logf("forked output ID: %s", ret.forkOutput.IDShort())

	ret.txBytes = make([][]byte, nConflicts)

	td := txbuilder2.NewTransferData(ret.privKey, ret.addr, ledger.LogicalTimeNow()).
		MustWithInputs(ret.forkOutput)

	vids := make([]*utangle_old.WrappedTx, 0)
	for i := 0; i < nConflicts; i++ {
		td.WithAmount(uint64(100 + i)).
			WithTargetLock(ret.addr)
		ret.txBytes[i], err = txbuilder2.MakeTransferTransaction(td)
		require.NoError(t, err)

		vDraft, err := ret.ut.MakeDraftVertexFromTxBytes(ret.txBytes[i])
		require.NoError(t, err)

		require.True(t, vDraft.IsSolid())

		vid, err := ret.ut.AppendVertex(vDraft, func() error {
			return txStore.PersistTxBytesWithMetadata(ret.txBytes[i])
		})
		if err != nil {
			if vid != nil {
				utangle_old.SaveGraphPastCone(vid, "make_vertex")
				t.Logf("***** failed transaction %d:\n%s\n*****", i, vid.Lines().String())
			}
		}
		if verbose {

		}
		if verbose {
			t.Logf("------ tx %d :\n%s\n", i, ret.ut.TransactionStringFromBytes(ret.txBytes[i]))
		}
		require.NoError(t, err)
		vids = append(vids, vid)
	}
	require.EqualValues(t, nConflicts, len(ret.txBytes))

	if verbose {
		for i, vid := range vids {
			t.Logf("======= past track of %d\n%s", i, vid.PastTrackLines().String())
		}
	}

	ret.outs = make([]*ledger.OutputWithID, nConflicts)
	ret.total = 0
	for i := range ret.outs {
		tx, err := transaction2.FromBytesMainChecksWithOpt(ret.txBytes[i])
		require.NoError(t, err)
		ret.outs[i] = tx.MustProducedOutputWithIDAt(1)
		require.EqualValues(t, 100+i, int(ret.outs[i].Output.Amount()))
		ret.total += ret.outs[i].Output.Amount()
	}

	return ret
}

type longConflictTestRunData struct {
	*conflictTestRunData
	txBytesSeq [][][]byte
	lastOuts   []*ledger.OutputWithID
}

func initLongConflictTest(t *testing.T, nConflicts int, howLong int, printTx bool) *longConflictTestRunData {
	ret := &longConflictTestRunData{}
	ret.conflictTestRunData = initConflictTest(t, nConflicts, printTx)

	txBytesSeq, err := txbuilder2.MakeTransactionSequences(howLong, ret.outs, ret.pkController)
	require.NoError(t, err)
	require.EqualValues(t, nConflicts, len(txBytesSeq))

	for i, txSeq := range txBytesSeq {
		for j, txBytes := range txSeq {
			_, txStr, err := ret.ut.AppendVertexFromTransactionBytesDebug(txBytes, func() error {
				return nil
			})
			if err != nil {
				t.Logf("seq %d, tx %d : %v\n%s", i, j, err, txStr)
			}
			require.NoError(t, err)
		}
	}

	ret.lastOuts = make([]*ledger.OutputWithID, nConflicts)
	for i := range ret.lastOuts {
		lastTx, err := transaction2.FromBytesMainChecksWithOpt(txBytesSeq[i][howLong-1])
		require.NoError(t, err)
		require.EqualValues(t, 1, lastTx.NumProducedOutputs())
		ret.lastOuts[i] = lastTx.MustProducedOutputWithIDAt(0)
		require.EqualValues(t, 100+i, ret.lastOuts[i].Output.Amount())
	}
	ret.txBytesSeq = txBytesSeq
	return ret
}

// TestBookingDoubleSpends1 produce N double spends, no problem with the tangle
func TestBookingDoubleSpends(t *testing.T) {
	t.Run("n double spends", func(t *testing.T) {
		const howMany = 10
		initConflictTest(t, howMany, false)
	})
	t.Run("conflict short", func(t *testing.T) {
		const howMany = 2
		const verbose = false
		it := initConflictTest(t, howMany, verbose)

		outs := make([]*ledger.OutputWithID, howMany)
		total := uint64(0)
		for i := range outs {
			tx, err := transaction2.FromBytesMainChecksWithOpt(it.txBytes[i])
			require.NoError(t, err)

			//t.Logf("------ tx #%d: \n%s\n", i, tx.ToString(it.ut.GetUTXO))

			outs[i] = tx.MustProducedOutputWithIDAt(1)
			require.EqualValues(t, 100+i, int(outs[i].Output.Amount()))
			total += outs[i].Output.Amount()
		}
		td := txbuilder2.NewTransferData(it.privKey, it.addr, ledger.LogicalTimeNow())
		td.MustWithInputs(outs...).
			WithAmount(total).
			WithTargetLock(it.addr)
		txBytesOut, err := txbuilder2.MakeTransferTransaction(td)
		require.NoError(t, err)

		if verbose {
			t.Logf("------ double spending tx: \n%s\n", it.ut.TransactionStringFromBytes(txBytesOut))
		}

		_, err = it.ut.MakeDraftVertexFromTxBytes(txBytesOut)
		t.Logf("expected error: '%v'", err)
		util.RequirePanicOrErrorWith(t, func() error { return err }, "conflict", it.forkOutput.IDShort())
		t.Logf("UTXOTangle at the end:\n%s", it.ut.Info())
	})
	t.Run("conflict long", func(t *testing.T) {
		const (
			howMany = 2 // 5
			howLong = 3 // 10
			verbose = false
		)
		it := initLongConflictTest(t, howMany, howLong, false)

		td := txbuilder2.NewTransferData(it.privKey, it.addr, ledger.LogicalTimeNow())
		td.MustWithInputs(it.lastOuts...).
			WithAmount(it.total).
			WithTargetLock(it.addr)
		txBytesOut, err := txbuilder2.MakeTransferTransaction(td)
		require.NoError(t, err)

		if verbose {
			t.Logf("------ double spending tx: \n%s\n", it.ut.TransactionStringFromBytes(txBytesOut))
		}

		_, err = it.ut.MakeDraftVertexFromTxBytes(txBytesOut)
		t.Logf("expected error: '%v'", err)
		util.RequirePanicOrErrorWith(t, func() error { return err }, "conflict", it.forkOutput.IDShort())
		t.Logf("UTXOTangle at the end:\n%s", it.ut.Info())
		t.Logf("UTXOTangle at the end:\n%s", it.ut.Info())
	})
}

func TestEndorsements1(t *testing.T) {
	t.Run("endorse fail", func(t *testing.T) {
		// endorsing non-sequencer transaction is not allowed
		const (
			howMany = 5
			howLong = 200
		)
		it := initLongConflictTest(t, howMany, howLong, false)

		// last one won't be consumed but endorsed
		endorseTxid := it.lastOuts[howMany-1].ID.TransactionID()
		total := it.total - it.lastOuts[howMany-1].Output.Amount()

		td := txbuilder2.NewTransferData(it.privKey, it.addr, ledger.LogicalTimeNow())
		td.MustWithInputs(it.lastOuts[:howMany-1]...).
			WithEndorsements(&endorseTxid).
			WithAmount(total).
			WithTargetLock(it.addr)
		txBytesOut, err := txbuilder2.MakeTransferTransaction(td)
		if err != nil {
			// it can happen to cross the slot boundary
			require.Contains(t, err.Error(), "can't endorse transaction from another slot")
			t.Logf("warning: generated endorsement crosses slot boundary")
			return
		}

		t.Logf("------ double spending tx: \n%s\n", transaction2.ParseBytesToString(txBytesOut, it.ut.GetUTXO))

		_, err = it.ut.MakeDraftVertexFromTxBytes(txBytesOut)
		require.Contains(t, err.Error(), "non-sequencer tx can't contain endorsements")
	})
	t.Run("check txbuilder no endorse cross slot", func(t *testing.T) {
		const (
			nConflicts = 2
			howLong    = 100
		)
		it := initLongConflictTest(t, nConflicts, howLong, false)
		// endorsed first tx
		endorseTxid, _, err := transaction2.IDAndTimestampFromTransactionBytes(it.txBytes[nConflicts-1])
		total := it.total - it.lastOuts[nConflicts-1].Output.Amount()

		// testing if tx builder allows incorrect endorsements
		td := txbuilder2.NewTransferData(it.privKey, it.addr, ledger.LogicalTimeNow())
		td.MustWithInputs(it.lastOuts[:nConflicts-1]...).
			WithEndorsements(&endorseTxid).
			WithAmount(total).
			WithTargetLock(it.addr)

		util.RequirePanicOrErrorWith(t, func() error {
			_, err = txbuilder2.MakeTransferTransaction(td)
			return err
		}, "can't endorse transaction from another time slot")
	})
}

type multiChainTestData struct {
	t                  *testing.T
	ts                 ledger.LogicalTime
	ut                 *utangle_old.UTXOTangle
	txBytesStore       global.TxBytesStore
	bootstrapChainID   ledger.ChainID
	privKey            ed25519.PrivateKey
	addr               ledger.AddressED25519
	faucetPrivKey      ed25519.PrivateKey
	faucetAddr         ledger.AddressED25519
	faucetOrigin       *ledger.OutputWithID
	sPar               genesis.LedgerIdentityData
	originBranchTxid   ledger.TransactionID
	txBytesChainOrigin []byte
	txBytes            [][]byte // with chain origins
	chainOrigins       []*ledger.OutputWithChainID
	total              uint64
	pkController       []ed25519.PrivateKey
}

const onChainAmount = 2_000_000

func initMultiChainTest(t *testing.T, nChains int, printTx bool) *multiChainTestData {
	t.Logf("initMultiChainTest: now is: %s, %v", ledger.LogicalTimeNow().String(), time.Now())
	ret := &multiChainTestData{t: t}
	var privKeys []ed25519.PrivateKey
	var addrs []ledger.AddressED25519

	genesisPrivKey := testutil.GetTestingPrivateKey()
	distrib, privKeys, addrs := inittest.GenesisParamsWithPreDistributionOld(2, onChainAmount*uint64(nChains))
	ret.sPar = *genesis.DefaultIdentityData(genesisPrivKey)
	ret.privKey = privKeys[0]
	ret.addr = addrs[0]
	ret.faucetPrivKey = privKeys[1]
	ret.faucetAddr = addrs[1]

	ret.pkController = make([]ed25519.PrivateKey, nChains)
	for i := range ret.pkController {
		ret.pkController[i] = ret.privKey
	}

	stateStore := common.NewInMemoryKVStore()
	ret.txBytesStore = txstore.NewDummyTxBytesStore()

	ret.bootstrapChainID, _ = genesis.InitLedgerState(ret.sPar, stateStore)
	txBytes, err := txbuilder2.DistributeInitialSupply(stateStore, genesisPrivKey, distrib)
	require.NoError(t, err)

	_, err = ret.txBytesStore.PersistTxBytesWithMetadata(txBytes)
	require.NoError(t, err)

	ret.ut = utangle_old.Load(stateStore)

	ret.originBranchTxid, _, err = transaction2.IDAndTimestampFromTransactionBytes(txBytes)
	require.NoError(t, err)

	stateReader := ret.ut.HeaviestStateForLatestTimeSlot()

	t.Logf("state identity:\n%s", genesis.MustLedgerIdentityDataFromBytes(stateReader.MustLedgerIdentityBytes()).String())
	t.Logf("origin branch txid: %s", ret.originBranchTxid.StringShort())
	t.Logf("%s", ret.ut.Info())

	ret.faucetOrigin = &ledger.OutputWithID{
		ID:     ledger.NewOutputID(&ret.originBranchTxid, 0),
		Output: nil,
	}
	bal, _ := state.BalanceOnLock(stateReader, ret.addr)
	require.EqualValues(t, onChainAmount*int(nChains), int(bal))
	bal, _ = state.BalanceOnLock(stateReader, ret.faucetAddr)
	require.EqualValues(t, onChainAmount*int(nChains), int(bal))

	oDatas, err := stateReader.GetUTXOsLockedInAccount(ret.addr.AccountID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))

	firstOut, err := oDatas[0].Parse()
	require.NoError(t, err)
	require.EqualValues(t, onChainAmount*uint64(nChains), firstOut.Output.Amount())

	faucetDatas, err := stateReader.GetUTXOsLockedInAccount(ret.faucetAddr.AccountID())
	require.NoError(t, err)
	require.EqualValues(t, 1, len(oDatas))

	ret.faucetOrigin, err = faucetDatas[0].Parse()
	require.NoError(t, err)
	require.EqualValues(t, onChainAmount*uint64(nChains), ret.faucetOrigin.Output.Amount())

	// Create transaction with nChains new chain origins.
	// It is not a sequencer tx with many chain origins
	txb := txbuilder2.NewTransactionBuilder()
	_, err = txb.ConsumeOutput(firstOut.Output, firstOut.ID)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)

	ret.ts = firstOut.Timestamp().AddTicks(ledger.TransactionPaceInTicks)

	ret.chainOrigins = make([]*ledger.OutputWithChainID, nChains)
	for range ret.chainOrigins {
		o := ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(onChainAmount).WithLock(ret.addr)
			_, err := o.PushConstraint(ledger.NewChainOrigin().Bytes())
			require.NoError(t, err)
		})
		_, err = txb.ProduceOutput(o)
		require.NoError(t, err)
	}

	txb.TransactionData.Timestamp = ret.ts
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(ret.privKey)

	ret.txBytesChainOrigin = txb.TransactionData.Bytes()

	tx, err := transaction2.FromBytesMainChecksWithOpt(ret.txBytesChainOrigin)
	require.NoError(t, err)

	if printTx {
		t.Logf("chain origin tx: %s", tx.ToString(stateReader.GetUTXO))
	}

	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid *ledger.OutputID) bool {
		out := ledger.OutputWithID{
			ID:     *oid,
			Output: o,
		}
		if int(idx) != nChains {
			chainID, ok := out.ExtractChainID()
			require.True(t, ok)
			ret.chainOrigins[idx] = &ledger.OutputWithChainID{
				OutputWithID: out,
				ChainID:      chainID,
			}
		}
		return true
	})

	if printTx {
		cstr := make([]string, 0)
		for _, o := range ret.chainOrigins {
			cstr = append(cstr, o.ChainID.StringShort())
		}
		t.Logf("Chain IDs:\n%s\n", strings.Join(cstr, "\n"))
	}

	_, _, err = ret.ut.AppendVertexFromTransactionBytesDebug(ret.txBytesChainOrigin, func() error {
		return ret.txBytesStore.PersistTxBytesWithMetadata(ret.txBytesChainOrigin)
	})
	require.NoError(t, err)
	return ret
}

func (r *multiChainTestData) createSequencerChain1(chainIdx int, pace int, printtx bool, exitFun func(i int, tx *transaction2.Transaction) bool) [][]byte {
	require.True(r.t, pace >= ledger.TransactionPaceInTicks*2)

	ret := make([][]byte, 0)
	outConsumeChain := r.chainOrigins[chainIdx]
	r.t.Logf("chain #%d, ID: %s, origin: %s", chainIdx, outConsumeChain.ChainID.StringShort(), outConsumeChain.ID.StringShort())
	chainID := outConsumeChain.ChainID

	par := txbuilder2.MakeSequencerTransactionParams{
		ChainInput:        outConsumeChain,
		StemInput:         nil,
		Timestamp:         outConsumeChain.Timestamp(),
		MinimumFee:        0,
		AdditionalInputs:  nil,
		AdditionalOutputs: nil,
		Endorsements:      nil,
		PrivateKey:        r.privKey,
		TotalSupply:       0,
	}

	lastStem := r.ut.HeaviestStemOutput()
	//r.t.Logf("lastStem #0 = %s, ts: %s", lastStem.ID.StringShort(), par.LogicalTime.String())
	lastBranchID := r.originBranchTxid

	var tx *transaction2.Transaction
	for i := 0; !exitFun(i, tx); i++ {
		prevTs := par.Timestamp
		toNext := par.Timestamp.TimesTicksToNextSlotBoundary()
		if toNext == 0 || toNext > pace {
			par.Timestamp = par.Timestamp.AddTicks(pace)
		} else {
			par.Timestamp = par.Timestamp.NextTimeSlotBoundary()
		}
		curTs := par.Timestamp
		//r.t.Logf("       %s -> %s", prevTs.String(), curTs.String())

		par.StemInput = nil
		if par.Timestamp.Tick() == 0 {
			par.StemInput = lastStem
		}

		par.Endorsements = nil
		if !par.ChainInput.ID.SequencerFlagON() {
			par.Endorsements = []*ledger.TransactionID{&lastBranchID}
		}

		txBytes, err := txbuilder2.MakeSequencerTransaction(par)
		require.NoError(r.t, err)
		ret = append(ret, txBytes)
		require.NoError(r.t, err)

		tx, err = transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)

		if printtx {
			ce := ""
			if prevTs.Slot() != curTs.Slot() {
				ce = "(cross-slot)"
			}
			r.t.Logf("tx %d : %s    %s", i, tx.IDShortString(), ce)
		}

		require.True(r.t, tx.IsSequencerMilestone())
		if par.StemInput != nil {
			require.True(r.t, tx.IsBranchTransaction())
		}

		o := tx.FindChainOutput(chainID)
		require.True(r.t, o != nil)

		par.ChainInput.OutputWithID = *o.Clone()
		if par.StemInput != nil {
			lastStem = tx.FindStemProducedOutput()
			require.True(r.t, lastStem != nil)
			//r.t.Logf("lastStem #%d = %s", i, lastStem.ID.StringShort())
		}
	}
	return ret
}

func TestMultiChain(t *testing.T) {
	t.Run("one chain", func(t *testing.T) {
		const (
			nChains              = 1
			howLong              = 5
			chainPaceInTimeSlots = 23
			printBranchTx        = false
		)
		r := initMultiChainTest(t, nChains, true)

		txBytesSeq := r.createSequencerChain1(0, chainPaceInTimeSlots, true, func(i int, tx *transaction2.Transaction) bool {
			return i == howLong
		})
		require.EqualValues(t, howLong, len(txBytesSeq))

		transaction2.SetPrintEasyFLTraceOnFail(false)

		for i, txBytes := range txBytesSeq {
			tx, err := transaction2.FromBytes(txBytes)
			require.NoError(r.t, err)
			if tx.IsBranchTransaction() {
				t.Logf("append %d txid = %s <-- branch transaction", i, tx.IDShortString())
			} else {
				t.Logf("append %d txid = %s", i, tx.IDShortString())
			}
			if tx.IsBranchTransaction() {
				if printBranchTx {
					t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
				}
			}
			vid, _, err := r.ut.AppendVertexFromTransactionBytesDebug(txBytes, func() error {
				return r.txBytesStore.PersistTxBytesWithMetadata(txBytes)
			})
			if err != nil {
				utangle_old.SaveGraphPastCone(vid, "failed")
			}
			require.NoError(t, err)
		}
		t.Logf("UTXOTangle at the end:\n%s", r.ut.Info(true))
	})
	t.Run("several chains until branch", func(t *testing.T) {
		const (
			nChains              = 5
			chainPaceInTimeSlots = 13
			printBranchTx        = false
		)
		r := initMultiChainTest(t, nChains, false)

		txBytesSeq := make([][][]byte, nChains)
		for i := range txBytesSeq {
			txBytesSeq[i] = r.createSequencerChain1(i, chainPaceInTimeSlots+i, false, func(i int, tx *transaction2.Transaction) bool {
				// until first branch
				return i > 0 && tx.IsBranchTransaction()
			})
			t.Logf("seq %d, length: %d", i, len(txBytesSeq[i]))
		}

		transaction2.SetPrintEasyFLTraceOnFail(false)

		for seqIdx := range txBytesSeq {
			for i, txBytes := range txBytesSeq[seqIdx] {
				//r.t.Logf("tangle info: %s", r.ut.Info())
				tx, err := transaction2.FromBytes(txBytes)
				require.NoError(r.t, err)
				//if tx.IsBranchTransaction() {
				//	t.Logf("append seq = %d, # = %d txid = %s <-- branch transaction", seqIdx, i, tx.IDShortString())
				//} else {
				//	t.Logf("append seq = %d, # = %d txid = %s", seqIdx, i, tx.IDShortString())
				//}
				if tx.IsBranchTransaction() {
					if printBranchTx {
						t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
					}
				}
				if seqIdx == 1 && i == 7 {
					t.Logf("---")
				}
				_, txStr, err := r.ut.AppendVertexFromTransactionBytesDebug(txBytes, func() error {
					return r.txBytesStore.PersistTxBytesWithMetadata(txBytes)
				})
				if err != nil {
					t.Logf("================= failed tx ======================= %s", txStr)
				}
				require.NoError(r.t, err)
			}

		}
	})
	t.Run("endorse conflicting chain", func(t *testing.T) {
		const (
			nChains                = 2
			chainPaceInTimeSlots   = 30 // 7
			printBranchTx          = false
			atLeastNumTransactions = 2
		)
		r := initMultiChainTest(t, nChains, false)

		txBytesSeq := make([][][]byte, nChains)
		for i := range txBytesSeq {
			numBranches := 0
			txBytesSeq[i] = r.createSequencerChain1(i, chainPaceInTimeSlots, false, func(i int, tx *transaction2.Transaction) bool {
				// at least given length and first non branch tx
				if tx != nil && tx.IsBranchTransaction() {
					numBranches++
				}
				return i >= atLeastNumTransactions && numBranches > 0 && !tx.IsBranchTransaction()
			})
			t.Logf("seq %d, length: %d", i, len(txBytesSeq[i]))
		}

		transaction2.SetPrintEasyFLTraceOnFail(false)

		for seqIdx := range txBytesSeq {
			for i, txBytes := range txBytesSeq[seqIdx] {
				tx, err := transaction2.FromBytes(txBytes)
				require.NoError(r.t, err)
				//if tx.IsBranchTransaction() {
				//	t.Logf("append seq = %d, # = %d txid = %s <-- branch transaction", seqIdx, i, tx.IDShortString())
				//} else {
				//	t.Logf("append seq = %d, # = %d txid = %s", seqIdx, i, tx.IDShortString())
				//}
				if tx.IsBranchTransaction() {
					if printBranchTx {
						t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
					}
				}
				_, txStr, err := r.ut.AppendVertexFromTransactionBytesDebug(txBytes, func() error {
					return r.txBytesStore.PersistTxBytesWithMetadata(txBytes)
				})
				if err != nil {
					t.Logf("================= failed tx ======================= %s", txStr)
				}
				require.NoError(r.t, err)
			}
		}
		r.t.Logf("tangle info: %s", r.ut.Info())
		// take the last transaction of the second sequence
		txBytes := txBytesSeq[1][len(txBytesSeq[1])-1]
		txEndorser, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(t, err)
		require.True(t, txEndorser.IsSequencerMilestone())
		require.False(t, txEndorser.IsBranchTransaction())
		require.EqualValues(t, 1, txEndorser.NumProducedOutputs())
		out := txEndorser.MustProducedOutputWithIDAt(0)
		t.Logf("output to consume:\n%s", out.Short())

		idToBeEndorsed, tsToBeEndorsed, err := transaction2.IDAndTimestampFromTransactionBytes(txBytesSeq[0][len(txBytesSeq[0])-1])
		require.NoError(t, err)
		ts := ledger.MaxLogicalTime(tsToBeEndorsed, txEndorser.Timestamp())
		ts = ts.AddTicks(ledger.TransactionPaceInTicks)
		t.Logf("timestamp to be endorsed: %s, endorser's timestamp: %s", tsToBeEndorsed.String(), ts.String())
		require.True(t, ts.Tick() != 0 && ts.Slot() == txEndorser.Timestamp().Slot())
		t.Logf("ID to be endorsed: %s", idToBeEndorsed.StringShort())

		txBytes, err = txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput: &ledger.OutputWithChainID{
				OutputWithID: *out,
				ChainID:      r.chainOrigins[1].ChainID,
			},
			Timestamp:    ts,
			Endorsements: []*ledger.TransactionID{&idToBeEndorsed},
			PrivateKey:   r.privKey,
		})
		require.NoError(t, err)
		util.RequirePanicOrErrorWith(t, func() error {
			vid, _, err := r.ut.AppendVertexFromTransactionBytesDebug(txBytes, func() error {
				return r.txBytesStore.PersistTxBytesWithMetadata(txBytes)
			})
			if err == nil && vid != nil {
				t.Logf("\n%s", vid.PastTrackLines().String())
				utangle_old.SaveGraphPastCone(vid, "err_expected")
			}
			// t.Logf("==============================\n%s", txStr)
			return err
		}, "conflict")
	})
	t.Run("cross endorsing chains 1", func(t *testing.T) {
		const (
			nChains              = 10
			chainPaceInTimeSlots = 7
			printBranchTx        = false
			howLong              = 90 // 400
		)
		r := initMultiChainTest(t, nChains, false)

		txBytesSeq := r.createSequencerChains1(chainPaceInTimeSlots, howLong)

		transaction2.SetPrintEasyFLTraceOnFail(false)

		for i, txBytes := range txBytesSeq {
			tx, err := transaction2.FromBytes(txBytes)
			require.NoError(r.t, err)
			//if tx.IsBranchTransaction() {
			//	t.Logf("append seq = %d, # = %d txid = %s <-- branch transaction", seqIdx, i, tx.IDShortString())
			//} else {
			//	t.Logf("append seq = %d, # = %d txid = %s", seqIdx, i, tx.IDShortString())
			//}
			if tx.IsBranchTransaction() {
				if printBranchTx {
					t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
				}
			}
			vid, txStr, err := r.ut.AppendVertexFromTransactionBytesDebug(txBytes, func() error {
				return r.txBytesStore.PersistTxBytesWithMetadata(txBytes)
			})
			if err != nil {
				t.Logf("================= failed tx ======================= %s", txStr)
				if vid != nil {
					utangle_old.SaveGraphPastCone(vid, "failedPastCone")
				}
			}
			require.NoError(r.t, err)
		}
		r.t.Logf("tangle info: %s", r.ut.Info())
	})
	t.Run("cross multi-endorsing chains", func(t *testing.T) {
		const (
			nChains              = 5
			chainPaceInTimeSlots = 7
			printBranchTx        = false
			howLong              = 1000
		)
		r := initMultiChainTest(t, nChains, false)

		txBytesSeq := r.createSequencerChains2(chainPaceInTimeSlots, howLong)

		transaction2.SetPrintEasyFLTraceOnFail(false)

		for i, txBytes := range txBytesSeq {
			tx, err := transaction2.FromBytes(txBytes)
			require.NoError(r.t, err)
			//if tx.IsBranchTransaction() {
			//	t.Logf("append seq = %d, # = %d txid = %s <-- branch transaction", seqIdx, i, tx.IDShortString())
			//} else {
			//	t.Logf("append seq = %d, # = %d txid = %s", seqIdx, i, tx.IDShortString())
			//}
			if tx.IsBranchTransaction() {
				if printBranchTx {
					t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
				}
			}
			_, txStr, err := r.ut.AppendVertexFromTransactionBytesDebug(txBytes, func() error {
				return r.txBytesStore.PersistTxBytesWithMetadata(txBytes)
			})
			if err != nil {
				t.Logf("================= failed tx ======================= %s", txStr)
			}
			require.NoError(r.t, err)
		}
		r.t.Logf("tangle info: %s", r.ut.Info())
	})
	t.Run("cross multi-endorsing chains with fees", func(t *testing.T) {
		const (
			nChains              = 5
			chainPaceInTimeSlots = 7
			printBranchTx        = false
			printTx              = true
			howLong              = 504 // 505 fails due to not enough tokens in the faucet
		)
		r := initMultiChainTest(t, nChains, false)

		txBytesSeq := r.createSequencerChains3(chainPaceInTimeSlots, howLong, printTx)

		transaction2.SetPrintEasyFLTraceOnFail(false)

		for i, txBytes := range txBytesSeq {
			tx, err := transaction2.FromBytes(txBytes)
			require.NoError(r.t, err)
			//if tx.IsBranchTransaction() {
			//	t.Logf("append seq = %d, # = %d txid = %s <-- branch transaction", seqIdx, i, tx.IDShortString())
			//} else {
			//	t.Logf("append seq = %d, # = %d txid = %s", seqIdx, i, tx.IDShortString())
			//}
			if tx.IsBranchTransaction() {
				if printBranchTx {
					t.Logf("branch tx %d : %s", i, transaction2.ParseBytesToString(txBytes, r.ut.GetUTXO))
				}
			}
			_, txStr, err := r.ut.AppendVertexFromTransactionBytesDebug(txBytes, func() error {
				return r.txBytesStore.PersistTxBytesWithMetadata(txBytes)
			})
			if err != nil {
				t.Logf("================= failed tx ======================= %s", txStr)
			}
			require.NoError(r.t, err)
		}
		r.t.Logf("tangle info: %s", r.ut.Info())
		//r.ut.SaveGraph("tangleExample")
	})
}

// n parallel sequencer chains. Each chain endorses one previous, if possible
func (r *multiChainTestData) createSequencerChains1(pace int, howLong int) [][]byte {
	require.True(r.t, pace >= ledger.TransactionPaceInTicks*2)
	nChains := len(r.chainOrigins)
	require.True(r.t, nChains >= 2)

	ret := make([][]byte, 0)
	sequences := make([][]*transaction2.Transaction, nChains)
	counter := 0
	for range sequences {
		// sequencer tx
		txBytes, err := txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput:   r.chainOrigins[counter],
			Timestamp:    r.chainOrigins[counter].Timestamp().AddTicks(pace),
			Endorsements: []*ledger.TransactionID{&r.originBranchTxid},
			PrivateKey:   r.privKey,
		})
		require.NoError(r.t, err)
		tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		sequences[counter] = []*transaction2.Transaction{tx}
		ret = append(ret, txBytes)
		r.t.Logf("chain #%d, ID: %s, origin: %s, seq start: %s",
			counter, r.chainOrigins[counter].ChainID.StringShort(), r.chainOrigins[counter].ID.StringShort(), tx.IDShortString())
		counter++
	}

	lastInChain := func(chainIdx int) *transaction2.Transaction {
		return sequences[chainIdx][len(sequences[chainIdx])-1]
	}

	lastStemOutput := r.ut.HeaviestStemOutput()

	var curChainIdx, nextChainIdx int
	var txBytes []byte
	var err error

	for i := counter; i < howLong; i++ {
		nextChainIdx = (curChainIdx + 1) % nChains
		ts := ledger.MaxLogicalTime(
			lastInChain(nextChainIdx).Timestamp().AddTicks(pace),
			lastInChain(curChainIdx).Timestamp().AddTicks(ledger.TransactionPaceInTicks),
		)
		chainIn := lastInChain(nextChainIdx).MustProducedOutputWithIDAt(0)

		if ts.TimesTicksToNextSlotBoundary() < 2*pace {
			ts = ts.NextTimeSlotBoundary()
		}
		var endorse []*ledger.TransactionID
		var stemOut *ledger.OutputWithID

		if ts.Tick() == 0 {
			// create branch tx
			stemOut = lastStemOutput
		} else {
			// endorse previous sequencer tx
			endorse = []*ledger.TransactionID{lastInChain(curChainIdx).ID()}
		}
		txBytes, err = txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput: &ledger.OutputWithChainID{
				OutputWithID: *chainIn,
				ChainID:      r.chainOrigins[nextChainIdx].ChainID,
			},
			StemInput:    stemOut,
			Endorsements: endorse,
			Timestamp:    ts,
			PrivateKey:   r.privKey,
		})
		require.NoError(r.t, err)
		tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		sequences[nextChainIdx] = append(sequences[nextChainIdx], tx)
		ret = append(ret, txBytes)
		if stemOut != nil {
			lastStemOutput = tx.FindStemProducedOutput()
		}

		if stemOut == nil {
			r.t.Logf("%d : chain #%d, txid: %s, endorse(%d): %s, timestamp: %s",
				i, nextChainIdx, tx.IDShortString(), curChainIdx, endorse[0].StringShort(), tx.Timestamp().String())
		} else {
			r.t.Logf("%d : chain #%d, txid: %s, timestamp: %s <- branch tx",
				i, nextChainIdx, tx.IDShortString(), tx.Timestamp().String())
		}
		curChainIdx = nextChainIdx
	}
	return ret
}

// n parallel sequencer chains. Each sequencer transaction endorses 1 or 2 previous if possible
func (r *multiChainTestData) createSequencerChains2(pace int, howLong int) [][]byte {
	require.True(r.t, pace >= ledger.TransactionPaceInTicks*2)
	nChains := len(r.chainOrigins)
	require.True(r.t, nChains >= 2)

	ret := make([][]byte, 0)
	sequences := make([][]*transaction2.Transaction, nChains)
	counter := 0
	for range sequences {
		txBytes, err := txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput:   r.chainOrigins[counter],
			Timestamp:    r.chainOrigins[counter].Timestamp().AddTicks(pace),
			Endorsements: []*ledger.TransactionID{&r.originBranchTxid},
			PrivateKey:   r.privKey,
		})
		require.NoError(r.t, err)
		tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		sequences[counter] = []*transaction2.Transaction{tx}
		ret = append(ret, txBytes)
		r.t.Logf("chain #%d, ID: %s, origin: %s, seq start: %s",
			counter, r.chainOrigins[counter].ChainID.StringShort(), r.chainOrigins[counter].ID.StringShort(), tx.IDShortString())
		counter++
	}

	lastInChain := func(chainIdx int) *transaction2.Transaction {
		return sequences[chainIdx][len(sequences[chainIdx])-1]
	}

	lastStemOutput := r.ut.HeaviestStemOutput()

	var curChainIdx, nextChainIdx int
	var txBytes []byte
	var err error

	for i := counter; i < howLong; i++ {
		nextChainIdx = (curChainIdx + 1) % nChains
		ts := ledger.MaxLogicalTime(
			lastInChain(nextChainIdx).Timestamp().AddTicks(pace),
			lastInChain(curChainIdx).Timestamp().AddTicks(ledger.TransactionPaceInTicks),
		)
		chainIn := lastInChain(nextChainIdx).MustProducedOutputWithIDAt(0)

		if ts.TimesTicksToNextSlotBoundary() < 2*pace {
			ts = ts.NextTimeSlotBoundary()
		}
		endorse := make([]*ledger.TransactionID, 0)
		var stemOut *ledger.OutputWithID

		if ts.Tick() == 0 {
			// create branch tx
			stemOut = lastStemOutput
		} else {
			// endorse previous sequencer tx
			const B = 4
			endorse = endorse[:0]
			endorsedIdx := curChainIdx
			maxEndorsements := B
			if maxEndorsements > nChains {
				maxEndorsements = nChains
			}
			for k := 0; k < maxEndorsements; k++ {
				endorse = append(endorse, lastInChain(endorsedIdx).ID())
				if endorsedIdx == 0 {
					endorsedIdx = nChains - 1
				} else {
					endorsedIdx--
				}
				if lastInChain(endorsedIdx).TimeSlot() != ts.Slot() {
					break
				}
			}
		}
		txBytes, err = txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput: &ledger.OutputWithChainID{
				OutputWithID: *chainIn,
				ChainID:      r.chainOrigins[nextChainIdx].ChainID,
			},
			StemInput:    stemOut,
			Endorsements: endorse,
			Timestamp:    ts,
			PrivateKey:   r.privKey,
		})
		require.NoError(r.t, err)
		tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		sequences[nextChainIdx] = append(sequences[nextChainIdx], tx)
		ret = append(ret, txBytes)
		if stemOut != nil {
			lastStemOutput = tx.FindStemProducedOutput()
		}

		if stemOut == nil {
			lst := make([]string, 0)
			for _, txid := range endorse {
				lst = append(lst, txid.StringShort())
			}
			r.t.Logf("%d : chain #%d, txid: %s, ts: %s, endorse: (%s)",
				i, nextChainIdx, tx.IDShortString(), tx.Timestamp().String(), strings.Join(lst, ","))
		} else {
			r.t.Logf("%d : chain #%d, txid: %s, ts: %s <- branch tx",
				i, nextChainIdx, tx.IDShortString(), tx.Timestamp().String())
		}
		curChainIdx = nextChainIdx
	}
	return ret
}

// n parallel sequencer chains. Each sequencer transaction endorses 1 or 2 previous if possible
// adding faucet transactions in between
func (r *multiChainTestData) createSequencerChains3(pace int, howLong int, printTx bool) [][]byte {
	require.True(r.t, pace >= ledger.TransactionPaceInTicks*2)
	nChains := len(r.chainOrigins)
	require.True(r.t, nChains >= 2)

	ret := make([][]byte, 0)
	sequences := make([][]*transaction2.Transaction, nChains)
	counter := 0
	for range sequences {
		txBytes, err := txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput:   r.chainOrigins[counter],
			Timestamp:    r.chainOrigins[counter].Timestamp().AddTicks(pace),
			Endorsements: []*ledger.TransactionID{&r.originBranchTxid},
			PrivateKey:   r.privKey,
		})
		require.NoError(r.t, err)
		tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		sequences[counter] = []*transaction2.Transaction{tx}
		ret = append(ret, txBytes)
		if printTx {
			r.t.Logf("chain #%d, ID: %s, origin: %s, seq start: %s",
				counter, r.chainOrigins[counter].ChainID.StringShort(), r.chainOrigins[counter].ID.StringShort(), tx.IDShortString())
		}
		counter++
	}

	faucetOutput := r.faucetOrigin

	lastInChain := func(chainIdx int) *transaction2.Transaction {
		return sequences[chainIdx][len(sequences[chainIdx])-1]
	}

	lastStemOutput := r.ut.HeaviestStemOutput()

	var curChainIdx, nextChainIdx int
	var txBytes []byte
	var tx *transaction2.Transaction
	var err error

	for i := counter; i < howLong; i++ {
		nextChainIdx = (curChainIdx + 1) % nChains
		// create faucet tx
		td := txbuilder2.NewTransferData(r.faucetPrivKey, r.faucetAddr, faucetOutput.Timestamp().AddTicks(ledger.TransactionPaceInTicks))
		td.WithTargetLock(ledger.ChainLockFromChainID(r.chainOrigins[nextChainIdx].ChainID)).
			WithAmount(100).
			MustWithInputs(faucetOutput)
		txBytes, err = txbuilder2.MakeTransferTransaction(td)
		require.NoError(r.t, err)
		tx, err = transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		faucetOutput = tx.MustProducedOutputWithIDAt(0)
		feeOutput := tx.MustProducedOutputWithIDAt(1)
		ret = append(ret, txBytes)
		if printTx {
			r.t.Logf("faucet tx %s: amount left on faucet: %d", tx.IDShortString(), faucetOutput.Output.Amount())
		}

		ts := ledger.MaxLogicalTime(
			lastInChain(nextChainIdx).Timestamp().AddTicks(pace),
			lastInChain(curChainIdx).Timestamp().AddTicks(ledger.TransactionPaceInTicks),
			tx.Timestamp().AddTicks(ledger.TransactionPaceInTicks),
		)
		chainIn := lastInChain(nextChainIdx).MustProducedOutputWithIDAt(0)

		if ts.TimesTicksToNextSlotBoundary() < 2*pace {
			ts = ts.NextTimeSlotBoundary()
		}
		endorse := make([]*ledger.TransactionID, 0)
		var stemOut *ledger.OutputWithID

		if ts.Tick() == 0 {
			// create branch tx
			stemOut = lastStemOutput
		} else {
			// endorse previous sequencer tx
			const B = 4
			endorse = endorse[:0]
			endorsedIdx := curChainIdx
			maxEndorsements := B
			if maxEndorsements > nChains {
				maxEndorsements = nChains
			}
			for k := 0; k < maxEndorsements; k++ {
				endorse = append(endorse, lastInChain(endorsedIdx).ID())
				if endorsedIdx == 0 {
					endorsedIdx = nChains - 1
				} else {
					endorsedIdx--
				}
				if lastInChain(endorsedIdx).TimeSlot() != ts.Slot() {
					break
				}
			}
		}
		txBytes, err = txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput: &ledger.OutputWithChainID{
				OutputWithID: *chainIn,
				ChainID:      r.chainOrigins[nextChainIdx].ChainID,
			},
			StemInput:        stemOut,
			AdditionalInputs: []*ledger.OutputWithID{feeOutput},
			Endorsements:     endorse,
			Timestamp:        ts,
			PrivateKey:       r.privKey,
		})
		require.NoError(r.t, err)
		tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)
		sequences[nextChainIdx] = append(sequences[nextChainIdx], tx)
		ret = append(ret, txBytes)
		if stemOut != nil {
			lastStemOutput = tx.FindStemProducedOutput()
		}

		if printTx {
			total := lastInChain(nextChainIdx).MustProducedOutputWithIDAt(0).Output.Amount()
			if stemOut == nil {
				lst := make([]string, 0)
				for _, txid := range endorse {
					lst = append(lst, txid.StringShort())
				}
				r.t.Logf("%d : chain #%d, txid: %s, ts: %s, total: %d, endorse: (%s)",
					i, nextChainIdx, tx.IDShortString(), tx.Timestamp().String(), total, strings.Join(lst, ","))
			} else {
				r.t.Logf("%d : chain #%d, txid: %s, ts: %s, total: %d <- branch tx",
					i, nextChainIdx, tx.IDShortString(), tx.Timestamp().String(), total)
			}
		}
		curChainIdx = nextChainIdx
	}
	return ret
}

//
//func TestInflation(t *testing.T) {
//	t.Run("fixed inflation", func(t *testing.T) {
//		const (
//			chainPaceInTimeSlots = 20
//			printBranchTx        = false
//			howLong              = 6 // 400
//			fixedInflation       = 100
//		)
//		r := initMultiChainTest(t, 1, false)
//
//		txBytesSeq := r.create1SequencerChain(chainPaceInTimeSlots, howLong, func() uint64 {
//			return fixedInflation
//		})
//
//		transaction.SetPrintEasyFLTraceOnFail(true)
//
//		branchCount := 0
//		var lastBranchTxStr string
//		for i, txBytes := range txBytesSeq {
//			tx, err := transaction.FromBytes(txBytes)
//			require.NoError(r.t, err)
//			if tx.IsBranchTransaction() {
//				if printBranchTx {
//					t.Logf("branch tx %d : %s", i, transaction.ParseBytesToString(txBytes, r.ut.GetUTXO))
//				}
//			}
//			opts := make([]utangle_old.ValidationOption, 0)
//			vid, txStr, err := r.ut.AppendVertexFromTransactionBytesDebug(txBytes, opts...)
//			if err != nil {
//				t.Logf("================= failed tx ======================= %s", txStr)
//				if vid != nil {
//					utangle_old.SaveGraphPastCone(vid, "failedPastCone")
//				}
//			}
//			require.NoError(r.t, err)
//			if vid.IsBranchTransaction() {
//				lastBranchTxStr = txStr
//				branchCount++
//				require.EqualValues(t, onChainAmount+branchCount*fixedInflation, tx.TotalAmount())
//			}
//		}
//		t.Logf("============ last branch tx ================\n%s", lastBranchTxStr)
//		t.Logf("tangle info: %s", r.ut.Info())
//	})
//	t.Run("random inflation", func(t *testing.T) {
//		const (
//			chainPaceInTimeSlots = 20
//			printBranchTx        = false
//			howLong              = 400
//		)
//		r := initMultiChainTest(t, 1, false)
//
//		txBytesSeq := r.create1SequencerChain(chainPaceInTimeSlots, howLong, func() uint64 {
//			return uint64(10 + rand.Intn(50))
//		})
//
//		transaction.SetPrintEasyFLTraceOnFail(false)
//
//		lastBranchTxStr := ""
//		var lastTx *transaction.Transaction
//		sumInflation := uint64(0)
//
//		for i, txBytes := range txBytesSeq {
//			tx, err := transaction.FromBytesMainChecksWithOpt(txBytes)
//			require.NoError(t, err)
//			if tx.IsBranchTransaction() {
//				if printBranchTx {
//					t.Logf("branch tx %d : %s", i, transaction.ParseBytesToString(txBytes, r.ut.GetUTXO))
//				}
//				sumInflation += tx.SequencerTransactionData().StemOutputData.InflationAmount
//			}
//			vid, txStr, err := r.ut.AppendVertexFromTransactionBytesDebug(txBytes)
//			if err != nil {
//				t.Logf("================= failed tx ======================= %s", txStr)
//				if vid != nil {
//					utangle_old.SaveGraphPastCone(vid, "failedPastCone")
//				}
//			}
//			require.NoError(t, err)
//			lastTx = tx
//			if vid.IsBranchTransaction() {
//				lastBranchTxStr = txStr
//			}
//		}
//
//		lastChainOut := lastTx.FindChainOutput(r.chainOrigins[0].ChainID)
//
//		require.EqualValues(t, int(sumInflation+onChainAmount), lastChainOut.Output.Amount())
//
//		t.Logf("============ last branch tx ================\n%s", lastBranchTxStr)
//		//t.Logf("tangle info: %s", r.ut.Info())
//	})
//}

// n parallel sequencer chains. Each chain endorses one previous, if possible
// Branch transactions make inflation
func (r *multiChainTestData) create1SequencerChain(pace int, howLong int, inflation func() uint64) [][]byte {
	require.True(r.t, pace >= ledger.TransactionPaceInTicks*2)

	ret := make([][]byte, 0)
	// sequencer tx
	txBytes, err := txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
		ChainInput:   r.chainOrigins[0],
		Timestamp:    r.chainOrigins[0].Timestamp().AddTicks(pace),
		Endorsements: []*ledger.TransactionID{&r.originBranchTxid},
		PrivateKey:   r.privKey,
	})
	require.NoError(r.t, err)
	tx, err := transaction2.FromBytesMainChecksWithOpt(txBytes)
	require.NoError(r.t, err)
	r.t.Logf("chainID: %s, origin: %s, seq start: %s",
		r.chainOrigins[0].ChainID.StringShort(), r.chainOrigins[0].ID.StringShort(), tx.IDShortString())

	lastStemOutput := r.ut.HeaviestStemOutput()
	lastInChain := tx
	ret = append(ret, txBytes)
	for i := 0; i < howLong; i++ {
		ts := ledger.MaxLogicalTime(
			lastInChain.Timestamp().AddTicks(pace),
			lastInChain.Timestamp().AddTicks(ledger.TransactionPaceInTicks),
		)
		chainIn := lastInChain.MustProducedOutputWithIDAt(0)
		if ts.TimesTicksToNextSlotBoundary() < 2*pace {
			ts = ts.NextTimeSlotBoundary()
		}
		var stemOut *ledger.OutputWithID
		var inflationAmount uint64
		if ts.Tick() == 0 {
			// create branch tx
			stemOut = lastStemOutput
			inflationAmount = inflation()
		}
		txBytes, err = txbuilder2.MakeSequencerTransaction(txbuilder2.MakeSequencerTransactionParams{
			ChainInput: &ledger.OutputWithChainID{
				OutputWithID: *chainIn,
				ChainID:      r.chainOrigins[0].ChainID,
			},
			StemInput:  stemOut,
			Inflation:  inflationAmount,
			Timestamp:  ts,
			PrivateKey: r.privKey,
		})
		require.NoError(r.t, err)
		tx, err = transaction2.FromBytesMainChecksWithOpt(txBytes)
		require.NoError(r.t, err)

		ret = append(ret, txBytes)
		lastInChain = tx
		if stemOut != nil {
			lastStemOutput = tx.FindStemProducedOutput()
		}
		if stemOut == nil {
			r.t.Logf("%3d: %s", i, tx.IDShortString())
		} else {
			r.t.Logf("%3d: %s <- branch tx", i, tx.IDShortString())
		}
	}

	return ret
}
