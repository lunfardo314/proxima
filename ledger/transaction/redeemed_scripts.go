package transaction

// IsScriptRedeemed reports whether a local-script hash has been committed by
// a prior redeemScript constraint in this tx. Linear scan over the slice;
// typical txs carry 0 entries, redeemer-using txs 1-2.
func (tx *Transaction) IsScriptRedeemed(h [32]byte) bool {
	for i := range tx.redeemedScripts {
		if tx.redeemedScripts[i] == h {
			return true
		}
	}
	return false
}

// AddRedeemedScript appends a local-script hash to the commitment list.
// Idempotent — duplicates are skipped.
func (tx *Transaction) AddRedeemedScript(h [32]byte) {
	if tx.IsScriptRedeemed(h) {
		return
	}
	tx.redeemedScripts = append(tx.redeemedScripts, h)
}
