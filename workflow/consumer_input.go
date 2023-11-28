package workflow

import (
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util/eventtype"
	"github.com/lunfardo314/proxima/util/seenset"
)

// PrimaryInputConsumer is where transaction enters the workflow pipeline

const PrimaryInputConsumerName = "input"

// PrimaryTransactionData is a basic data of the raw transaction
type (
	TransactionSourceType byte

	// PrimaryTransactionData is an input message type for this consumer
	PrimaryTransactionData struct {
		Tx            *transaction.Transaction
		SourceType    TransactionSourceType
		ReceivedFrom  peer.ID
		ReceivedWhen  time.Time
		WasGossiped   bool
		WasPulled     bool
		eventCallback func(event string, data any)
		traceFlag     bool
	}

	TransactionInOption func(*PrimaryTransactionData)

	PrimaryConsumer struct {
		*Consumer[*PrimaryTransactionData]
		seen *seenset.SeenSet[core.TransactionIDVeryShort8]
	}
)

const (
	TransactionSourceTypeAPI = TransactionSourceType(iota)
	TransactionSourceTypeSequencer
	TransactionSourceTypePeer
	TransactionSourceTypeStore
)

func (t TransactionSourceType) String() string {
	switch t {
	case TransactionSourceTypeAPI:
		return "API"
	case TransactionSourceTypeSequencer:
		return "sequencer"
	case TransactionSourceTypePeer:
		return "peer"
	case TransactionSourceTypeStore:
		return "txStore"
	default:
		return "(unknown tx source)"
	}
}

// EventCodeDuplicateTx this consumer rises the event with transaction ID as a parameter whenever duplicate is detected
var EventCodeDuplicateTx = eventtype.RegisterNew[*core.TransactionID]("duplicateTx")

// initPrimaryInputConsumer initializes the consumer
func (w *Workflow) initPrimaryInputConsumer() {
	c := &PrimaryConsumer{
		Consumer: NewConsumer[*PrimaryTransactionData](PrimaryInputConsumerName, w),
		seen:     seenset.New[core.TransactionIDVeryShort8](),
	}
	c.AddOnConsume(func(inp *PrimaryTransactionData) {
		// tracing every input message
		c.Debugf(inp, "IN")
	})
	c.AddOnConsume(c.consume) // process input
	c.AddOnClosed(func() {
		// cleanup downstream on close
		w.pullConsumer.Stop()
		w.preValidateConsumer.Stop()
		w.pullRequestConsumer.Stop()

		w.terminateWG.Done()
	})

	nmDuplicate := EventCodeDuplicateTx.String()
	w.MustOnEvent(EventCodeDuplicateTx, func(txid *core.TransactionID) {
		// log duplicate transaction upon event
		c.glb.IncCounter(c.Name() + "." + nmDuplicate)
		c.Log().Debugf("%s: %s", nmDuplicate, txid.StringShort())
	})
	// the consumer is globally known in the workflow
	w.primaryInputConsumer = c
}

// consume processes the input
func (c *PrimaryConsumer) consume(inp *PrimaryTransactionData) {
	inp.eventCallback(PrimaryInputConsumerName+".in", inp.Tx)

	c.traceTx(inp, "IN")

	// the input is pre-parsed transaction with base validation ok.
	//It means it has full ID, so it is identifiable as a transaction
	if c.isDuplicate(inp.Tx.ID()) {
		// if duplicate, rise the event
		inp.eventCallback("finish."+PrimaryInputConsumerName, fmt.Errorf("duplicate %s", inp.Tx.IDShort()))
		c.glb.PostEvent(EventCodeDuplicateTx, inp.Tx.ID())
		return
	}

	c.glb.IncCounter(c.Name() + ".out")
	// passes identifiable transaction which is not a duplicate to the pre-validation consumer
	c.glb.preValidateConsumer.Push(&PreValidateConsumerInputData{
		PrimaryTransactionData: inp,
	})
}

func (c *PrimaryConsumer) isDuplicate(txid *core.TransactionID) bool {
	if c.seen.Seen(txid.VeryShortID8()) {
		c.glb.IncCounter(c.Name() + ".duplicate.seen")
		c.Log().Debugf("isInPullList seen -- " + txid.String())
		return true
	}
	return false
}
