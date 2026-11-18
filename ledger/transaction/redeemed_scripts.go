package transaction

import (
	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
)

// redeemedScript is one entry of the per-tx commitment list. The decoded
// script is kept with the hash so callRedeemer never depends on the shared
// library cache, whose content other validations change concurrently.
type redeemedScript struct {
	hash   [32]byte
	script *easyfl.LocalScript[*ledger.EvalContext]
}

// IsScriptRedeemed reports whether a local-script hash has been committed by
// a prior redeemScript constraint in this tx.
func (tx *Transaction) IsScriptRedeemed(h [32]byte) bool {
	_, ok := tx.RedeemedScript(h)
	return ok
}

// RedeemedScript returns the decoded script committed under hash h by this tx.
// Linear scan: typical txs carry 0 entries, redeemer-using txs 1-2.
func (tx *Transaction) RedeemedScript(h [32]byte) (*easyfl.LocalScript[*ledger.EvalContext], bool) {
	for i := range tx.redeemedScripts {
		if tx.redeemedScripts[i].hash == h {
			return tx.redeemedScripts[i].script, true
		}
	}
	return nil, false
}

// AddRedeemedScript appends a local-script hash and its decoded script to the
// commitment list. Idempotent — duplicates are skipped.
func (tx *Transaction) AddRedeemedScript(h [32]byte, s *easyfl.LocalScript[*ledger.EvalContext]) {
	if tx.IsScriptRedeemed(h) {
		return
	}
	tx.redeemedScripts = append(tx.redeemedScripts, redeemedScript{hash: h, script: s})
}

// NumRedeemedScripts returns how many distinct local scripts this tx has
// committed to.
func (tx *Transaction) NumRedeemedScripts() int {
	return len(tx.redeemedScripts)
}
