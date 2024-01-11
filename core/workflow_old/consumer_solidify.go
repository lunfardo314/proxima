package workflow_old

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	utangle "github.com/lunfardo314/proxima/utangle_old"
	"github.com/lunfardo314/proxima/util"
)

const SolidifyConsumerName = "solidify"

const (
	SolidifyCommandNewTx = byte(iota)
	SolidifyCommandAddedTx
	SolidifyCommandDroppedTx
	SolidifyCommandRemoveTooOld
)

type (
	SolidifyInputData struct {
		Cmd byte
		// != nil for SolidifyCommandAddedTx and SolidifyCommandDroppedTx, == nil for SolidifyCommandNewTx
		TxID *ledger.TransactionID
		// == nil for SolidifyCommandAddedTx and SolidifyCommandDroppedTx, != nil for SolidifyCommandNewTx
		*PrimaryTransactionData
	}

	SolidifyConsumer struct {
		*Consumer[*SolidifyInputData]
		stopSolidificationDeadlineLoopChan chan struct{}
		// txPending is list of draft dag waiting for solidification to be sent for validation
		txPending map[ledger.TransactionID]wantedTx
	}

	wantedTx struct {
		since time.Time // since when wantedTx
		// if != nil, it is transaction being solidified
		// if == nil, transaction is unknown yet
		draftVertexData *draftVertexData
		// tx IDs who are waiting for the tx to be solid
		whoIsWaitingList []ledger.TransactionID
	}

	draftVertexData struct {
		*PrimaryTransactionData
		vertex                     *utangle.Vertex
		numMissingInputTxs         uint16
		stemInputAlreadyPulled     bool
		sequencerPredAlreadyPulled bool
		allInputsAlreadyPulled     bool
	}
)

const solidificationTimeout = 3 * time.Minute // TODO only for testing. Must be longer in reality

func (w *Workflow) initSolidifyConsumer() {
	ret := &SolidifyConsumer{
		Consumer:                           NewConsumer[*SolidifyInputData](SolidifyConsumerName, w),
		txPending:                          make(map[ledger.TransactionID]wantedTx),
		stopSolidificationDeadlineLoopChan: make(chan struct{}),
	}
	ret.AddOnConsume(ret.consume)
	ret.AddOnClosed(func() {
		close(ret.stopSolidificationDeadlineLoopChan)
		w.validateConsumer.Stop()
	})
	w.solidifyConsumer = ret
	go ret.solidificationDeadlineLoop()
}

func (c *SolidifyConsumer) consume(inp *SolidifyInputData) {
	switch inp.Cmd {
	case SolidifyCommandNewTx:
		util.Assertf(inp.TxID == nil && inp.PrimaryTransactionData != nil, "inp.TxID == nil && inp.primaryInput != nil")
		c.traceTx(inp.PrimaryTransactionData, "cmd newTx")
		c.glb.IncCounter(c.Name() + ".in.new")
		c.newTx(inp)

	case SolidifyCommandAddedTx:
		util.Assertf(inp.TxID != nil && inp.PrimaryTransactionData == nil, "inp.TxID != nil && inp.primaryInput == nil")
		c.traceTxID(inp.TxID, "cmd addedTx")
		c.glb.IncCounter(c.Name() + ".in.added")
		c.addedTx(inp.TxID)

	case SolidifyCommandDroppedTx:
		util.Assertf(inp.TxID != nil && inp.PrimaryTransactionData == nil, "inp.TxID != nil && inp.primaryInput == nil")
		c.traceTxID(inp.TxID, "cmd dropTxID")
		c.glb.IncCounter(c.Name() + ".in.remove")
		c.dropTxID(*inp.TxID)

	case SolidifyCommandRemoveTooOld:
		util.Assertf(inp.TxID == nil && inp.PrimaryTransactionData == nil, "inp.TxID != nil && inp.primaryInput == nil")
		c.removeTooOld()

	default:
		panic("wrong solidifier command")
	}
}

func (c *SolidifyConsumer) newTx(inp *SolidifyInputData) {
	txid := inp.tx.ID()
	pendingData, exists := c.txPending[*txid]
	util.Assertf(pendingData.draftVertexData == nil, "newTx: unexpected repeating new tx in solidifier: %s", txid.StringShort())
	if !exists {
		pendingData.since = time.Now()
	}

	pendingData.draftVertexData = &draftVertexData{
		PrimaryTransactionData: inp.PrimaryTransactionData,
		vertex:                 utangle.NewVertex(inp.tx),
	}

	// try to solidify. It fetches what is available
	if !c.runSolidification(pendingData.draftVertexData) {
		return
	}

	// get set of missing transaction IDs
	missing := pendingData.draftVertexData.vertex.MissingInputTxIDSet()

	pendingData.draftVertexData.numMissingInputTxs = uint16(len(missing))
	if pendingData.draftVertexData.numMissingInputTxs == 0 {
		// solid already. No need for solidification, send directly to validation
		c.sendForValidation(pendingData.draftVertexData.PrimaryTransactionData, pendingData.draftVertexData.vertex)
		c.traceTx(pendingData.draftVertexData.PrimaryTransactionData, "newTx: send for validation")
		return
	}
	c.traceTx(pendingData.draftVertexData.PrimaryTransactionData, "input transactions missing: %d (total inputs: %d, endorsements: %d)",
		pendingData.draftVertexData.numMissingInputTxs, pendingData.draftVertexData.tx.NumInputs(), pendingData.draftVertexData.tx.NumEndorsements())

	// pull what is missing. For branches and sequencer milestones it is a staged process
	c.pullIfNeeded(pendingData.draftVertexData)

	// make waiting lists
	missing.ForEach(func(missingTxID ledger.TransactionID) bool {
		c.putIntoWhoIsWaitingList(missingTxID, *txid)
		return true
	})
	// add to the pending list
	c.txPending[*txid] = pendingData
}

func (c *SolidifyConsumer) addedTx(txid *ledger.TransactionID) {
	pendingData, isKnown := c.txPending[*txid]
	if !isKnown {
		c.traceTxID(txid, "addedTx: not in solidifier -> ignore")
		// nobody is waiting, nothing to remove. Ignore
		return
	}
	util.Assertf(pendingData.draftVertexData == nil || pendingData.draftVertexData.vertex.IsSolid(),
		"pendingData.draftVertexData == nil || pendingData.draftVertexData.vertex.IsSolid()")

	delete(c.txPending, *txid)
	// check all transactions in the waiting list
	for _, waitingTxID := range pendingData.whoIsWaitingList {
		c.checkTx(waitingTxID)
	}
}

// checkTx check how many missing inputs left.
// If missing inputs counter == 0, finalizes solidification of the transaction and passes it to validator
func (c *SolidifyConsumer) checkTx(txid ledger.TransactionID) {
	pendingData, isKnown := c.txPending[txid]
	if !isKnown {
		c.traceTxID(&txid, "checkTx: unknown tx")
		// nobody is waiting. Ignore
		return
	}
	util.Assertf(pendingData.draftVertexData != nil, "pendingData.draftVertexData != nil")
	if pendingData.draftVertexData.numMissingInputTxs == 0 {
		// already sent for validation
		return
	}
	pendingData.draftVertexData.numMissingInputTxs--
	c.traceTx(pendingData.draftVertexData.PrimaryTransactionData,
		"checkTx: number of remaining missing inputs: %d", pendingData.draftVertexData.numMissingInputTxs)

	if pendingData.draftVertexData.numMissingInputTxs > 0 {
		// there left some missing input txs. Just pull if needed
		c.pullIfNeeded(pendingData.draftVertexData)
		return
	}
	// all needed inputs are booked already. Finish solidification
	c.traceTx(pendingData.draftVertexData.PrimaryTransactionData, "checkTx: finish solidification")
	if !c.runSolidification(pendingData.draftVertexData) {
		c.dropTxID(txid)
		return
	}
	util.Assertf(pendingData.draftVertexData.vertex.IsSolid(), "must be solid %s", txid.StringShort())

	c.sendForValidation(pendingData.draftVertexData.PrimaryTransactionData, pendingData.draftVertexData.vertex)
	c.traceTx(pendingData.draftVertexData.PrimaryTransactionData, "checkTx: send for validation")
}

func (c *SolidifyConsumer) runSolidification(vd *draftVertexData) bool {
	conflict := vd.vertex.FetchMissingDependencies(c.glb.utxoTangle)
	if conflict != nil {
		// there's conflict in the past cone. Cannot be solidified. Remove from solidifier
		c.traceTx(vd.PrimaryTransactionData, "conflict", conflict.StringShort())
		txid := vd.PrimaryTransactionData.tx.ID()
		c.dropTxID(*txid)
		c.glb.PostEventDropTxID(txid, c.Name(), "conflict %s", conflict.StringShort())
		if vd.vertex.Tx.IsBranchTransaction() {
			c.glb.utxoTangle.SyncData().UnEvidenceIncomingBranch(*txid)
		}
		vd.PrimaryTransactionData.eventCallback("finish."+SolidifyConsumerName, fmt.Errorf("conflict %s", conflict.StringShort()))
		return false
	}

	if vd.tx.IsSequencerMilestone() {
		vd.sequencerPredAlreadyPulled = vd.sequencerPredAlreadyPulled || vd.vertex.IsSequencerInputSolid()
	}
	if vd.tx.IsBranchTransaction() {
		vd.stemInputAlreadyPulled = vd.stemInputAlreadyPulled || vd.vertex.IsStemInputSolid()
	}
	return true
}

// dropTxID recursively removes txid from solidifier and all txid which directly or indirectly waiting for it
func (c *SolidifyConsumer) dropTxID(txid ledger.TransactionID) {
	pendingData, isKnown := c.txPending[txid]
	if !isKnown {
		return
	}
	delete(c.txPending, txid)
	txid1 := txid
	c.traceTxID(&txid1, "dropTxID: deleted")
	for _, delTxID := range pendingData.whoIsWaitingList {
		c.dropTxID(delTxID)
	}
}

func (c *SolidifyConsumer) removeTooOld() {
	nowis := time.Now()
	toRemove := make([]ledger.TransactionID, 0)
	for txid, pendingData := range c.txPending {
		if nowis.After(pendingData.since.Add(solidificationTimeout)) {
			txid1 := txid
			toRemove = append(toRemove, txid1)
			c.glb.PostEventDropTxID(&txid1, c.Name(), "deadline. Missing: %s", c.missingInputsString(txid1))
			if pendingData.draftVertexData != nil && pendingData.draftVertexData.vertex.Tx.IsBranchTransaction() {
				c.glb.utxoTangle.SyncData().UnEvidenceIncomingBranch(txid1)
			}
		}
	}
	for i := range toRemove {
		txid1 := toRemove[i]
		c.glb.pullConsumer.stopPulling(&txid1)
		c.dropTxID(txid1)
	}
}

func (c *SolidifyConsumer) sendForValidation(primaryTxData *PrimaryTransactionData, draftVertex *utangle.Vertex) {
	util.Assertf(draftVertex.IsSolid(), "v.IsSolid()")
	c.glb.validateConsumer.Push(&ValidateConsumerInputData{
		PrimaryTransactionData: primaryTxData,
		draftVertex:            draftVertex,
	})
}

// putIntoWhoIsWaitingList each missing txid has entry in txPending. Each entry has list of transaction
// which are waiting for it. The function puts missingTxID into the corresponding list. It creates entry if it is new
func (c *SolidifyConsumer) putIntoWhoIsWaitingList(missingTxID, whoIsWaiting ledger.TransactionID) {
	var waitingLst []ledger.TransactionID
	pendingData, exists := c.txPending[missingTxID]
	if exists {
		waitingLst = pendingData.whoIsWaitingList
	} else {
		// new wanted transaction, not seen yet
		pendingData.since = time.Now()
	}
	if len(waitingLst) == 0 {
		waitingLst = make([]ledger.TransactionID, 0, 2)
	}
	pendingData.whoIsWaitingList = append(waitingLst, whoIsWaiting)
	c.txPending[missingTxID] = pendingData
}

// pullIfNeeded pulls missing inputs. For sequencer transactions it goes in steps
func (c *SolidifyConsumer) pullIfNeeded(vd *draftVertexData) {
	if vd.allInputsAlreadyPulled {
		return
	}
	neededFor := vd.tx.ID()

	stemInputTxID := func() ledger.TransactionID {
		return vd.tx.SequencerTransactionData().StemOutputData.PredecessorOutputID.TransactionID()
	}
	if vd.tx.IsBranchTransaction() && !vd.stemInputAlreadyPulled {
		// first need to solidify stem input. Only when stem input is solid, we pull the rest
		// this makes node synchronization more sequential, from past to present slot by slot,
		// because branch transactions are pulled first and along the chain
		vd.stemInputAlreadyPulled = true
		c.tracePull("pull stem transaction")
		c.pull(neededFor, stemInputTxID())
		return
	}
	seqPredTxID := func() ledger.TransactionID {
		seqInputIdx := vd.tx.SequencerTransactionData().SequencerOutputData.ChainConstraint.PredecessorInputIndex
		seqInputOID := vd.tx.MustInputAt(seqInputIdx)
		return seqInputOID.TransactionID()
	}
	// here stem input solid, if any
	if vd.tx.IsSequencerMilestone() && !vd.sequencerPredAlreadyPulled {
		vd.sequencerPredAlreadyPulled = true
		c.tracePull("pull sequencer predecessor transaction")
		c.pull(neededFor, seqPredTxID())
		return
	}
	// now we can pull the rest (except stem and sequencer predecessor, if any)
	allTheRest := vd.vertex.MissingInputTxIDSet()
	if vd.tx.IsBranchTransaction() {
		allTheRest.Remove(stemInputTxID())
	}
	if vd.tx.IsSequencerMilestone() {
		allTheRest.Remove(seqPredTxID())
	}

	c.traceTx(vd.PrimaryTransactionData, "pull the rest %d missing input transactions. Total inputs: %d, total endorsements: %d",
		len(allTheRest),
		vd.vertex.Tx.NumInputs(),
		vd.vertex.Tx.NumEndorsements(),
	)
	vd.allInputsAlreadyPulled = true
	c.pull(neededFor, util.Keys(allTheRest)...)
}

func (c *SolidifyConsumer) postDropTxID(txid *ledger.TransactionID) {
	c.Push(&SolidifyInputData{
		Cmd:  SolidifyCommandDroppedTx,
		TxID: txid,
	})
}

func (c *SolidifyConsumer) postAddedTxID(txid *ledger.TransactionID) {
	c.Push(&SolidifyInputData{
		Cmd:  SolidifyCommandAddedTx,
		TxID: txid,
	})
}

func (c *SolidifyConsumer) pull(neededFor *ledger.TransactionID, txids ...ledger.TransactionID) {
	for i := range txids {
		c.tracePull("submit pull %s. Needed for %s",
			func() any { return txids[i].StringShort() },
			func() any { return neededFor.StringShort() },
		)
	}

	c.glb.pullConsumer.Push(&PullTxData{
		TxIDs: txids,
	})
}

const solidificationDeadlineLoopPeriod = time.Second

func (c *SolidifyConsumer) solidificationDeadlineLoop() {
	defer c.Log().Debugf("solidification deadline loop stopped")

	syncStatus := c.glb.utxoTangle.SyncData()
	for {
		select {
		case <-c.stopSolidificationDeadlineLoopChan:
			return
		case <-time.After(solidificationDeadlineLoopPeriod):
			// invoke purge of too old if inside sync window. If node is not synced, be patient and do not remove old transactions
			if syncStatus.IsSynced() {
				c.Push(&SolidifyInputData{Cmd: SolidifyCommandRemoveTooOld})
			}
		}
	}
}

func (c *SolidifyConsumer) missingInputsString(txid ledger.TransactionID) string {
	if pendingData, found := c.txPending[txid]; found {
		if pendingData.draftVertexData != nil {
			return pendingData.draftVertexData.vertex.MissingInputTxIDString()
		} else {
			return fmt.Sprintf("%s is not in solidifier yet", txid.StringShort())
		}
	}
	return fmt.Sprintf("inconsistency: txid %s is not among pendingtx IDs", txid.StringShort())
}
