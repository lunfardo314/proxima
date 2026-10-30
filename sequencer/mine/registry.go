package mine

import (
	"sync"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

// Registry holds every mine transit this node has received, by the
// mine output it spends, and validates them on demand.
//
// The sequencer ranks a transit against all of its competitors, but only the
// transits addressed to it reach its backlog, and a non-sequencer transaction is
// validated only inside the attacher of a milestone that includes it. Without
// this registry a sequencer would see neither the competitors nor a valid
// transit of its own before someone else included it: it would never settle a
// slot, only follow whoever took transits early. Every node receives every
// transit through the mining-transaction event, before the attach gate.
//
// Validation is the ledger's own: the transaction re-parsed from its bytes and
// validated in full context against the output it spends, so the VRF output it
// yields is proven, not claimed. The memDAG is not touched.
type Registry struct {
	mutex  sync.Mutex
	byTxID map[base.TransactionID]*registeredTransit
	byPred map[base.OutputID][]*registeredTransit
	// newest slot seen, for pruning
	maxSlot uint32
	// fetchTx reads transaction bytes the registry does not hold (the tx store)
	fetchTx func(txid base.TransactionID) []byte
	// now is the node's ledger time, stamped on each transit as it arrives
	now func() base.LedgerTime
}

type registeredTransit struct {
	txBytes []byte
	desc    Transit // pending until validated
	checked bool    // validated or found invalid
	valid   bool
	// registered is the node's ledger time when the transit arrived: a rival
	// counts against another sequencer's inclusion only if it was known in time
	registered base.LedgerTime
}

// registrySlots is how far back transits are kept: the tag-along window,
// within which a transit can still be included
const registrySlots = 32

func NewRegistry(fetchTx func(txid base.TransactionID) []byte) *Registry {
	return &Registry{
		byTxID:  make(map[base.TransactionID]*registeredTransit),
		byPred:  make(map[base.OutputID][]*registeredTransit),
		fetchTx: fetchTx,
		now:     ledger.TimeNow,
	}
}

// Add registers a transit from its raw bytes. Called from the event dispatch, so
// it only parses: validation waits until the transit is ranked.
func (r *Registry) Add(txBytes []byte) {
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil || !tx.IsMiningTransaction() {
		return
	}
	desc, ok := TransitOfTx(tx)
	if !ok {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if _, already := r.byTxID[desc.TxID]; already {
		return
	}
	e := &registeredTransit{txBytes: txBytes, desc: desc, registered: r.now()}
	r.byTxID[desc.TxID] = e
	r.byPred[desc.Pred] = append(r.byPred[desc.Pred], e)
	if desc.Slot > r.maxSlot {
		r.maxSlot = desc.Slot
		r.pruneLocked()
	}
}

func (r *Registry) pruneLocked() {
	if r.maxSlot < registrySlots {
		return
	}
	horizon := r.maxSlot - registrySlots
	for txid, e := range r.byTxID {
		if e.desc.Slot < horizon {
			delete(r.byTxID, txid)
		}
	}
	for pred := range r.byPred {
		if pred.Slot() < horizon {
			delete(r.byPred, pred)
		}
	}
}

// Validated returns the descriptor of a registered transit with its proven VRF
// output, or false if the transit is unknown, invalid, or cannot be validated
// yet (the output it spends is not available).
func (r *Registry) Validated(txid base.TransactionID) (Transit, bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	e, ok := r.byTxID[txid]
	if !ok || !r.checkLocked(e) {
		return Transit{}, false
	}
	return e.desc, true
}

// Rivals returns the validated transits spending pred, except the one given,
// that are addressed to a sequencer for which active returns true.
func (r *Registry) Rivals(pred base.OutputID, except base.TransactionID, active func(seqID base.ChainID) bool) []Transit {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	var ret []Transit
	for _, e := range r.byPred[pred] {
		if e.desc.TxID == except || !active(e.desc.Target) || !r.checkLocked(e) {
			continue
		}
		ret = append(ret, e.desc)
	}
	return ret
}

// BetterRivalKnownBefore reports whether a validated transit spending the same
// output as m, addressed to an active sequencer and registered before the given
// ledger time, is a better solution than m: an older slot or a smaller VRF output.
// The txid tie-break is left out on purpose: a miner may send one solution as several
// transits, one per sequencer, and including any of them is including the best
// solution. m must carry its VRF output.
func (r *Registry) BetterRivalKnownBefore(m Transit, before base.LedgerTime, active func(seqID base.ChainID) bool) bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	for _, e := range r.byPred[m.Pred] {
		if e.desc.TxID == m.TxID || !e.registered.Before(before) || !active(e.desc.Target) || !r.checkLocked(e) {
			continue
		}
		if e.desc.betterSolutionThan(&m) {
			return true
		}
	}
	return false
}

// checkLocked validates the transit once. A transit whose spent output cannot
// be fetched yet stays unchecked and is tried again next time.
func (r *Registry) checkLocked(e *registeredTransit) bool {
	if e.checked {
		return e.valid
	}
	pred := e.desc.Pred
	predBytes := r.txBytesLocked(pred.TransactionID())
	if predBytes == nil {
		return false
	}
	predTx, err := transaction.ParseWithPartialValidation(predBytes)
	if err != nil || int(pred.Index()) >= predTx.NumProducedOutputs() {
		e.checked = true
		return false
	}
	tx, err := transaction.ParseWithPartialValidation(e.txBytes)
	if err == nil {
		err = tx.SetFullContextWithFetch(func(oid base.OutputID) ([]byte, bool) {
			if oid != pred {
				return nil, false
			}
			return predTx.MustOutputDataAt(oid.Index()), true
		})
	}
	if err == nil {
		err = tx.ValidateFullContext()
	}
	e.checked = true
	if err != nil {
		return false
	}
	beta, ok := tx.MineVRFOutput()
	if !ok {
		return false
	}
	e.desc.VRFOutput, e.desc.Pending, e.valid = beta, false, true
	return true
}

func (r *Registry) txBytesLocked(txid base.TransactionID) []byte {
	if e, ok := r.byTxID[txid]; ok {
		return e.txBytes
	}
	if r.fetchTx == nil {
		return nil
	}
	return r.fetchTx(txid)
}
