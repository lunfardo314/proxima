package mine

import (
	"sort"
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

// maxHashTransitsPerPred bounds the transits whose work is their transaction ID kept
// per predecessor: their work is known from the bytes, so only the best few are kept.
// Once K is far below the hashrate, honest miners alone find thousands of solutions.
const maxHashTransitsPerPred = 16

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
	if tx.MineWorkIsTxID() {
		desc.Work, _ = workOf(tx) // known from the ID, still pending until validated
	}
	e := &registeredTransit{txBytes: txBytes, desc: desc, registered: r.now()}
	r.byTxID[desc.TxID] = e
	r.byPred[desc.Pred] = append(r.byPred[desc.Pred], e)
	if tx.MineWorkIsTxID() {
		r.keepBestLocked(desc.Pred)
	}
	if desc.Slot > r.maxSlot {
		r.maxSlot = desc.Slot
		r.pruneLocked()
	}
}

// keepBestLocked drops the worst-ranked transits of pred beyond maxHashTransitsPerPred
func (r *Registry) keepBestLocked(pred base.OutputID) {
	list := r.byPred[pred]
	if len(list) <= maxHashTransitsPerPred {
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].desc.BetterThan(&list[j].desc) })
	for _, e := range list[maxHashTransitsPerPred:] {
		delete(r.byTxID, e.desc.TxID)
	}
	r.byPred[pred] = list[:maxHashTransitsPerPred]
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
		if e.desc.TxID == except || !e.desc.addressedTo(active) || !r.checkLocked(e) {
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
// solution. m must carry its work.
func (r *Registry) BetterRivalKnownBefore(m Transit, before base.LedgerTime, active func(seqID base.ChainID) bool) bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	for _, e := range r.byPred[m.Pred] {
		if e.desc.TxID == m.TxID || !e.registered.Before(before) || !e.desc.addressedTo(active) || !r.checkLocked(e) {
			continue
		}
		if e.desc.betterSolutionThan(&m) {
			return true
		}
	}
	return false
}

// TransitSpentBy returns the transaction-hash transit registered here whose output tx
// spends directly, validated if it can be: a transit can reach a sequencer through an
// ordinary transaction spending its reward, and is then ranked as if consumed directly.
// VRF transits keep the direct rule.
func (r *Registry) TransitSpentBy(tx *transaction.Transaction) (Transit, bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	for i := 0; i < tx.NumInputs(); i++ {
		oid := tx.MustInputAt(byte(i))
		e, ok := r.byTxID[oid.TransactionID()]
		if !ok || e.desc.Work == nil && e.desc.Pending {
			continue // not a registered hash transit
		}
		r.checkLocked(e)
		return e.desc, true
	}
	return Transit{}, false
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
	if err != nil || !tx.MineSuccessorIsPlain() {
		return false
	}
	w, ok := workOf(tx)
	if !ok {
		return false
	}
	e.desc.Work, e.desc.Pending, e.valid = w, false, true
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
