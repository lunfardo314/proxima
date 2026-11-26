package consolidator

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
)

// A cold run moves what the consolidator moves out under a second key
// (kb/cold_wallet.md). In key mode a move into a delegation or to a sequencer
// is a pair: the first transaction, signed by the hot key, carries the hot
// inputs to one cold sigLock and returns what is kept to the hot wallet; the
// second, signed by the cold key, spends that cold output alone and makes the
// move. The first has no tag-along, so it reaches a state only with the second.
// In address mode every move is one hot transaction.

// preTx is the first transaction of a pair, held until submit posts it just
// before the second.
type preTx struct {
	bytes    []byte
	consumed [][]byte
	txid     base.TransactionID
	moved    uint64
}

func (k *Consolidator) coldKeyPresent() bool {
	return k.cold != nil && k.cold.PrivateKey != nil
}

// asCold runs f with the cold key as the signer of what it builds.
func (k *Consolidator) asCold(f func() []base.OutputID) []base.OutputID {
	k.privateKey, k.holderID = k.cold.PrivateKey, k.cold.HolderID
	defer func() { k.privateKey, k.holderID = k.hotKey, k.hotID }()
	return f()
}

// asMaster runs f signing as the master of the managed delegations.
func (k *Consolidator) asMaster(f func() []base.OutputID) []base.OutputID {
	if k.cold == nil {
		return f()
	}
	return k.asCold(f)
}

// move makes one move of hot funds with f. In key mode it is a pair: f, signing
// as the cold wallet, builds the second transaction from the plan reduced to the
// first transaction's cold output. Returns the hot inputs consumed, or nil when
// no move was made this tick.
func (k *Consolidator) move(p *plan, f func(p *plan) []base.OutputID) []base.OutputID {
	if k.cold == nil || k.cold.AddressMode {
		return f(p)
	}
	defer func() { k.pre = nil }()
	if !k.coldKnown() {
		return nil
	}
	coldOut, err := k.viaCold(p.inputs, p.moved, p.kept)
	if err != nil {
		k.logf("move to the cold wallet deferred: %v", err)
		return nil
	}
	reduced := &plan{inputs: []*ledger.OutputWithID{coldOut}, consumed: p.moved, moved: p.moved}
	if k.asCold(func() []base.OutputID { return f(reduced) }) == nil {
		return nil
	}
	return outputIDs(p.inputs)
}

// viaCold builds the first transaction of a pair: inputs to one cold sigLock
// holding moved at output 0, and kept, when there is any, back to a hot sigLock.
// Signed by the hot key whoever the current signer is. Returns the cold output,
// the second transaction's input.
func (k *Consolidator) viaCold(inputs []*ledger.OutputWithID, moved, kept uint64) (*ledger.OutputWithID, error) {
	if moved < k.floor || (kept > 0 && kept < k.floor) {
		return nil, fmt.Errorf("%s to move and %s to keep: each must be at least the storage deposit %s of a sigLock output",
			util.Prox(moved), util.Prox(kept), util.Prox(k.floor))
	}
	txb := txbuildercore.New(k.consts.UpgradeIndex)
	consumed, newest, err := consumeInputs(txb, inputs, 0)
	if err != nil {
		return nil, err
	}
	coldOut, err := txbuildercore.NewSigLockOutput(k.lib, moved, k.cold.HolderID)
	if err != nil {
		return nil, err
	}
	txb.ProduceOutput(coldOut.Bytes())
	if kept > 0 {
		hotOut, err := txbuildercore.NewSigLockOutput(k.lib, kept, k.hotID)
		if err != nil {
			return nil, err
		}
		txb.ProduceOutput(hotOut.Bytes())
	}
	txb.SetTimestamp(k.timestamp(newest))
	txb.ComputeInputCommitment()
	txb.SignED25519(k.hotKey)
	txid, err := txbuildercore.TxIDFromBytes(txb.Bytes())
	util.AssertNoError(err) // pure local computation over bytes just built
	oid, err := base.NewOutputID(txid, 0)
	util.AssertNoError(err)
	o, err := ledger.OutputFromBytes(coldOut.Bytes())
	if err != nil {
		return nil, err
	}
	k.pre = &preTx{bytes: txb.Bytes(), consumed: consumed, txid: txid, moved: moved}
	return &ledger.OutputWithID{ID: oid, Output: o}, nil
}

// coldKnown reports whether the cold account owns an output in the LRB state.
// The ingate drops a transaction from a sender it does not know, so without one
// the second transaction of a pair would be lost.
func (k *Consolidator) coldKnown() bool {
	known, err := k.c.IsKnownController(ledger.SigLock(k.cold.HolderID).ControllerID())
	if err != nil {
		k.logf("move to the cold wallet deferred: %v", err)
		return false
	}
	if !known {
		k.logf("move to the cold wallet deferred: the cold account owns no output in the ledger state, so a transaction it signs would be dropped")
	}
	return known
}

// moveToCold moves what is above the minimum to a cold sigLock in one hot
// transaction, less the tag-along fee; what is kept returns to the hot wallet.
func (k *Consolidator) moveToCold(p *plan) []base.OutputID {
	if p.moved < k.tagAlongFee+k.floor {
		k.verbosef("nothing moved to the cold wallet: %s does not cover the tag-along fee %s plus the storage deposit %s",
			util.Prox(p.moved), util.Prox(k.tagAlongFee), util.Prox(k.floor))
		return nil
	}
	txb := txbuildercore.New(k.consts.UpgradeIndex)
	consumed, newest, err := consumeInputs(txb, p.inputs, 0)
	if err != nil {
		k.logf("move to the cold wallet build failed: %v", err)
		return nil
	}
	out, err := txbuildercore.NewSigLockOutput(k.lib, p.moved-k.tagAlongFee, k.cold.HolderID)
	if err != nil {
		k.logf("move to the cold wallet build failed: %v", err)
		return nil
	}
	txb.ProduceOutput(out.Bytes())
	if err = k.produceTagAlongAndKept(txb, p.kept); err != nil {
		k.logf("move to the cold wallet build failed: %v", err)
		return nil
	}
	txid := k.finish(txb, k.timestamp(newest))
	if err = k.submit(txb.Bytes(), consumed...); err != nil {
		k.logf("move to the cold wallet submit failed: %v", err)
		return nil
	}
	k.logf("consolidated %d output(s) holding %s: moved %s to the cold wallet, kept %s -> %s (submitted, not awaited)",
		len(p.inputs), util.Prox(p.consumed), util.Prox(p.moved-k.tagAlongFee), util.Prox(p.kept), txid.StringShort())
	return outputIDs(p.inputs)
}

// migrateDelegation moves one delegation still mastered by the hot key under
// the cold wallet: re-delegated on the master path, signed by the hot key, the
// successor naming masterID, the cold holder ID. The master path pins nothing on
// the successor. A frozen delegation waits until its master can spend it.
func (k *Consolidator) migrateDelegation(hotDels []*ownDelegation) []base.OutputID {
	market, err := k.activeSequencers()
	if err != nil {
		k.verbosef("hot delegations not checked: %v", err)
		return nil
	}
	k.classify(hotDels, market, k.nowSlot())
	for _, d := range hotDels {
		if d.consumable {
			d.stale = "moving it under the cold wallet as master"
			return k.retargetDelegation(d, hotDels, market)
		}
	}
	return nil
}
