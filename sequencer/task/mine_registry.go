package task

import (
	"sync"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

// MineTransitRegistry holds every mine transit this node has received, by the
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
type MineTransitRegistry struct {
	mutex  sync.Mutex
	byTxID map[base.TransactionID]*registeredTransit
	byPred map[base.OutputID][]*registeredTransit
	// newest slot seen, for pruning
	maxSlot uint32
	// fetchTx reads transaction bytes the registry does not hold (the tx store)
	fetchTx func(txid base.TransactionID) []byte
}

type registeredTransit struct {
	txBytes []byte
	desc    mineTransit // pending until validated
	checked bool        // validated or found invalid
	valid   bool
}

// mineRegistrySlots is how far back transits are kept: the tag-along window,
// within which a transit can still be included
const mineRegistrySlots = 32

func NewMineTransitRegistry(fetchTx func(txid base.TransactionID) []byte) *MineTransitRegistry {
	return &MineTransitRegistry{
		byTxID:  make(map[base.TransactionID]*registeredTransit),
		byPred:  make(map[base.OutputID][]*registeredTransit),
		fetchTx: fetchTx,
	}
}

// Add registers a transit from its raw bytes. Called from the event dispatch, so
// it only parses: validation waits until the transit is ranked.
func (r *MineTransitRegistry) Add(txBytes []byte) {
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil || !tx.IsMiningTransaction() {
		return
	}
	desc, ok := mineTransitOfTx(tx)
	if !ok {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if _, already := r.byTxID[desc.txid]; already {
		return
	}
	e := &registeredTransit{txBytes: txBytes, desc: desc}
	r.byTxID[desc.txid] = e
	r.byPred[desc.pred] = append(r.byPred[desc.pred], e)
	if desc.slot > r.maxSlot {
		r.maxSlot = desc.slot
		r.pruneLocked()
	}
}

func (r *MineTransitRegistry) pruneLocked() {
	if r.maxSlot < mineRegistrySlots {
		return
	}
	horizon := r.maxSlot - mineRegistrySlots
	for txid, e := range r.byTxID {
		if e.desc.slot < horizon {
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
func (r *MineTransitRegistry) Validated(txid base.TransactionID) (mineTransit, bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	e, ok := r.byTxID[txid]
	if !ok || !r.checkLocked(e) {
		return mineTransit{}, false
	}
	return e.desc, true
}

// Rivals returns the validated transits spending pred, except the one given,
// that are addressed to a sequencer for which active returns true.
func (r *MineTransitRegistry) Rivals(pred base.OutputID, except base.TransactionID, active func(seqID base.ChainID) bool) []mineTransit {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	var ret []mineTransit
	for _, e := range r.byPred[pred] {
		if e.desc.txid == except || !active(e.desc.target) || !r.checkLocked(e) {
			continue
		}
		ret = append(ret, e.desc)
	}
	return ret
}

// checkLocked validates the transit once. A transit whose spent output cannot
// be fetched yet stays unchecked and is tried again next time.
func (r *MineTransitRegistry) checkLocked(e *registeredTransit) bool {
	if e.checked {
		return e.valid
	}
	pred := e.desc.pred
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
	e.desc.vrfOutput, e.desc.pending, e.valid = beta, false, true
	return true
}

func (r *MineTransitRegistry) txBytesLocked(txid base.TransactionID) []byte {
	if e, ok := r.byTxID[txid]; ok {
		return e.txBytes
	}
	if r.fetchTx == nil {
		return nil
	}
	return r.fetchTx(txid)
}
