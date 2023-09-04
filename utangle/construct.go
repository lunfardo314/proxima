package utangle

import (
	"crypto/ed25519"
	"fmt"
	"sync"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/genesis"
	state "github.com/lunfardo314/proxima/state"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/proxima/util/txlog"
	"github.com/lunfardo314/unitrie/common"
	"go.uber.org/atomic"
)

type (
	UTXOTangle struct {
		mutex        sync.RWMutex
		stateStore   general.StateStore
		txBytesStore common.KVStore
		vertices     map[core.TransactionID]*WrappedTx
		branches     map[core.TimeSlot]map[*WrappedTx]branch

		lastPrunedOrphaned atomic.Time
		lastCutFinal       atomic.Time

		numAddedVertices   int
		numDeletedVertices int
		numAddedBranches   int
		numDeletedBranches int
	}

	Vertex struct {
		txLog        *txlog.TransactionLog
		Tx           *transaction.Transaction
		StateDelta   UTXOStateDelta //
		Inputs       []*WrappedTx
		Endorsements []*WrappedTx
	}

	UTXOStateDelta struct {
		baselineBranch *WrappedTx
		transactions   map[*WrappedTx]transactionData
		coverage       uint64
	}

	transactionData struct {
		consumed          set.Set[byte] // TODO optimize with bitmap?
		includedThisDelta bool
	}

	VirtualTransaction struct {
		txid             core.TransactionID
		mutex            sync.RWMutex
		outputs          map[byte]*core.Output
		sequencerOutputs *[2]byte // if nil, it is unknown
	}

	branch struct {
		root common.VCommitment
	}
)

const TipSlots = 5

func newUTXOTangle(stateStore general.StateStore, txBytesStore common.KVStore) *UTXOTangle {
	return &UTXOTangle{
		stateStore:   stateStore,
		txBytesStore: txBytesStore,
		vertices:     make(map[core.TransactionID]*WrappedTx),
		branches:     make(map[core.TimeSlot]map[*WrappedTx]branch),
	}
}

func InitGenesisState(par genesis.StateIdentityData, stateStore general.StateStore) (common.VCommitment, core.ChainID, *core.OutputWithID, *core.OutputWithID) {
	bootstrapSequencerID, genesisStateRoot := genesis.InitLedgerState(par, stateStore)
	// now genesisStateRoot contains origin chain and stem outputs
	// fetch origin chain and stem outputs
	genesisStateReader := state.MustNewSugaredStateReader(stateStore, genesisStateRoot)
	genesisOutputID := genesis.InitialSupplyOutputID(par.GenesisTimeSlot)
	genesisOutput := genesisStateReader.MustGetOutput(&genesisOutputID)
	genesisStemOutput := genesisStateReader.GetStemOutput()

	return genesisStateRoot, bootstrapSequencerID, genesisOutput, genesisStemOutput
}

func CreateGenesisUTXOTangle(par genesis.StateIdentityData, stateStore general.StateStore, txBytesStore common.KVStore) (*UTXOTangle, core.ChainID, common.VCommitment) {
	genesisStateRoot, bootstrapSequencerID, genesisOutput, genesisStemOutput := InitGenesisState(par, stateStore)

	// create virtual transaction for genesis outputs
	genesisVirtualTx := newVirtualTx(genesis.InitialSupplyTransactionID(par.GenesisTimeSlot))
	genesisVirtualTx.addOutput(genesis.InitialSupplyOutputIndex, genesisOutput.Output)
	genesisVirtualTx.addOutput(genesis.StemOutputIndex, genesisStemOutput.Output)
	genesisVirtualTx.addSequencerIndices(genesis.InitialSupplyOutputIndex, genesis.StemOutputIndex)
	genesisVID := genesisVirtualTx.Wrap()

	util.Assertf(genesisVID.IsBranchTransaction(), "genesisVID.IsBranchTransaction()")
	util.Assertf(genesisVID.TimeSlot() == par.GenesisTimeSlot, "genesisVID.TimeTick() == par.GenesisTimeSlot")

	// create genesis UTXO tangle object
	ret := newUTXOTangle(stateStore, txBytesStore)
	ret.addVertex(genesisVID)
	ret.addBranch(genesisVID, genesisStateRoot)

	return ret, bootstrapSequencerID, genesisStateRoot
}

func CreateGenesisUTXOTangleWithDistribution(par genesis.StateIdentityData, originPrivateKey ed25519.PrivateKey, genesisDistribution []txbuilder.LockBalance, stateStore general.StateStore, txBytesStore common.KVStore) (*UTXOTangle, core.ChainID, core.TransactionID) {
	pubKeyOrig := originPrivateKey.Public().(ed25519.PublicKey)
	util.Assertf(pubKeyOrig.Equal(par.GenesisControllerPublicKey), "inconsistent parameters")

	ret, bootstrapSequencerID, genesisStateRoot := CreateGenesisUTXOTangle(par, stateStore, txBytesStore)

	// now genesisStateRoot contains origin chain and stem outputs
	// fetch origin chain and stem outputs
	// init state with genesis outputs and make the distribution branch transaction
	distributionTxBytes := txbuilder.MakeDistributionTransaction(txbuilder.OriginDistributionParams{
		BootstrapSequencerID:        bootstrapSequencerID,
		StateStore:                  stateStore,
		GenesisStateRoot:            genesisStateRoot,
		GenesisControllerPrivateKey: originPrivateKey,
		InitialSupply:               par.InitialSupply,
		GenesisDistribution:         genesisDistribution,
	})

	// sanity check genesis outputs
	genesisStateReader := state.MustNewSugaredStateReader(stateStore, genesisStateRoot)
	genesisOutputID := genesis.InitialSupplyOutputID(par.GenesisTimeSlot)
	genesisOutput := genesisStateReader.MustGetOutput(&genesisOutputID)
	genesisStemOutput := genesisStateReader.GetStemOutput()

	stemBack := ret.HeaviestStemOutput()
	util.Assertf(stemBack.ID == genesisStemOutput.ID, "stemBack.ID == genesisStemOutput.ID")
	genesisBack, err := ret.HeaviestStateForLatestTimeSlot().GetOutput(&genesisOutput.ID)
	util.AssertNoError(err)
	util.Assertf(genesisBack.ID == genesisOutputID, "genesisBack.ID == genesisOutputID")

	// add distribution transaction
	distributionTxVID, err := ret.AppendVertexFromTransactionBytes(distributionTxBytes)
	util.AssertNoError(err)

	// store distribution transaction
	txBytesStore.Set(distributionTxVID.ID().Bytes(), distributionTxBytes)

	//fmt.Printf("++ delta of distribution tx:\n%s\n", distributionTxVID.DeltaString())

	stemBack = ret.HeaviestStemOutput()
	util.Assertf(stemBack.ID.TimeSlot() == genesisStemOutput.ID.TimeSlot()+1, "stemBack.ID.TimeTick() == genesisStemOutput.ID.TimeTick()+1")
	return ret, bootstrapSequencerID, *distributionTxVID.ID()
}

func (ut *UTXOTangle) AddVertex(vids ...*WrappedTx) {
	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	ut.addVertex(vids...)
}

func (ut *UTXOTangle) addVertex(vids ...*WrappedTx) {
	for _, vid := range vids {
		txid := vid.ID()
		_, already := ut.vertices[*txid]
		util.Assertf(!already, "addVertex: repeating transaction %s", txid.Short())
		ut.vertices[*txid] = vid
	}
	ut.numAddedVertices += len(vids)
}

func (ut *UTXOTangle) deleteVertex(txid *core.TransactionID) {
	_, ok := ut.vertices[*txid]
	if ok {
		ut.numDeletedVertices++
	}
	delete(ut.vertices, *txid)
}

func (ut *UTXOTangle) addBranch(branchVID *WrappedTx, root common.VCommitment) {
	m, exist := ut.branches[branchVID.TimeSlot()]
	if !exist {
		m = make(map[*WrappedTx]branch)
	}
	m[branchVID] = branch{
		root: root,
	}
	ut.branches[branchVID.TimeSlot()] = m
}

func (ut *UTXOTangle) SolidifyInputsFromTxBytes(txBytes []byte) (*Vertex, error) {
	tx, err := transaction.TransactionFromBytesAllChecks(txBytes)
	if err != nil {
		return nil, err
	}
	return ut.SolidifyInputs(tx)
}

func newVertex(tx *transaction.Transaction, txLog *txlog.TransactionLog) *Vertex {
	return &Vertex{
		Tx:           tx,
		txLog:        txLog,
		Inputs:       make([]*WrappedTx, tx.NumInputs()),
		Endorsements: make([]*WrappedTx, tx.NumEndorsements()),
		StateDelta:   *NewUTXOStateDelta(nil),
	}
}

func (ut *UTXOTangle) SolidifyInputs(tx *transaction.Transaction, txl ...*txlog.TransactionLog) (*Vertex, error) {
	var txLog *txlog.TransactionLog
	if len(txl) > 0 {
		txLog = txl[0]
	}
	ret := newVertex(tx, txLog)
	if err := ret.FetchMissingDependencies(ut); err != nil {
		return nil, err
	}
	return ret, nil
}

func _calcDelta(vid *WrappedTx) error {
	v, ok := vid.UnwrapVertex()
	util.Assertf(ok, "vertex expected")

	if err := v.mergeInputDeltas(); err != nil {
		return err
	}
	if conflict := v.StateDelta.include(vid); conflict != nil {
		return fmt.Errorf("conflict %s while including %s into delta:\n%s", conflict.IDShort(), vid.IDShort(), v.StateDelta.LinesRecursive().String())
	}
	return nil
}

func MakeVertex(draftVertex *Vertex, bypassConstraintValidation ...bool) (*WrappedTx, error) {
	if !draftVertex.IsSolid() {
		return nil, fmt.Errorf("some inputs or endorsements are not solid")
	}
	retVID := draftVertex.Wrap()

	if err := draftVertex.Validate(bypassConstraintValidation...); err != nil {
		return retVID, fmt.Errorf("validate %s : '%v'", draftVertex.Tx.IDShort(), err)
	}
	if err := _calcDelta(retVID); err != nil {
		return retVID, fmt.Errorf("MakeVertex: %v", err)
	}
	return retVID, nil
}

func (ut *UTXOTangle) AppendVertex(vid *WrappedTx) error {
	ut.mutex.Lock()
	defer ut.mutex.Unlock()

	ut.addVertex(vid)

	if vid.IsBranchTransaction() {
		if err := ut.finalizeBranch(vid); err != nil {
			SaveGraphPastCone(vid, "finalizeBranchError")
			return err
		}
	}
	return nil
}

func (ut *UTXOTangle) AppendVertexFromTransactionBytes(txBytes []byte) (*WrappedTx, error) {
	tx, err := transaction.TransactionFromBytesAllChecks(txBytes)
	if err != nil {
		return nil, err
	}
	vertexDraft, err := ut.SolidifyInputs(tx)
	if err != nil {
		return nil, err
	}
	ret, err := MakeVertex(vertexDraft)
	if err != nil {
		return nil, err
	}
	err = ut.AppendVertex(ret)
	return ret, err
}

// AppendVertexFromTransactionBytesDebug for testing mainly
func (ut *UTXOTangle) AppendVertexFromTransactionBytesDebug(txBytes []byte) (*WrappedTx, string, error) {
	vertexDraft, err := ut.SolidifyInputsFromTxBytes(txBytes)
	if err != nil {
		return nil, "", err
	}
	retTxStr := vertexDraft.String()

	ret, err := MakeVertex(vertexDraft)
	if err != nil {
		return ret, retTxStr, err
	}
	err = ut.AppendVertex(ret)
	retTxStr += "\n-------\n\n" + vertexDraft.ConsumedInputsToString()
	return ret, retTxStr, err
}

// finalizeBranch also starts new delta on mutations
func (ut *UTXOTangle) finalizeBranch(newBranchVertex *WrappedTx) error {
	util.Assertf(newBranchVertex.IsBranchTransaction(), "v.IsBranchTransaction()")

	var err error
	var newRoot common.VCommitment
	var nextStemOutputID core.OutputID

	newBranchVertex.Unwrap(UnwrapOptions{

		Vertex: func(v *Vertex) {
			seqData := v.Tx.SequencerTransactionData()
			nextStemOutputID = v.Tx.OutputID(seqData.StemOutputIndex)
			util.Assertf(v.StateDelta.baselineBranch != nil, "v.StateDelta.baselineBranch != nil")
			prevBranch, ok := ut.getBranch(v.StateDelta.baselineBranch)

			if !ok {
				SaveGraphPastCone(newBranchVertex, "branch_prob")
			}
			util.Assertf(ok, "finalizeBranch %s: can't find previous branch %s", newBranchVertex.IDShort(), v.StateDelta.baselineBranch.IDShort())

			upd := state.MustNewUpdatable(ut.stateStore, prevBranch.root)
			cmds := v.StateDelta.getUpdateCommands()

			err = upd.UpdateWithCommands(cmds, &nextStemOutputID, &seqData.SequencerID)
			util.Assertf(err == nil, "finalizeBranch %s: '%v'\n=== Delta: %s\n=== Commands: %s",
				v.Tx.IDShort(), err, v.StateDelta.LinesRecursive().String(), state.UpdateCommandsToLines(cmds))
			newRoot = upd.Root()
		},

		VirtualTx: func(_ *VirtualTransaction) {
			util.Assertf(false, "finalizeBranch: must be a branch vertex")
		},
	})
	if err != nil {
		return err
	}

	// assert consistency
	rdr, err := state.NewSugaredReadableState(ut.stateStore, newRoot)
	util.AssertNoError(err)
	util.Assertf(rdr.GetStemOutput().ID == nextStemOutputID, "rdr.GetStemOutput().ID == nextStemOutputID")

	// store new branch to the tangle data structure
	branches := ut.branches[newBranchVertex.TimeSlot()]
	if len(branches) == 0 {
		branches = make(map[*WrappedTx]branch)
		ut.branches[newBranchVertex.TimeSlot()] = branches
	}
	branches[newBranchVertex] = branch{
		root: newRoot,
	}
	ut.numAddedBranches++
	return nil
}
