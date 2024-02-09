package ledger

// not thread-safe. Set once upon startup, later read-only

var (
	ledgerIDSingleton     *IdentityData
	ledgerIDHashSingleton [32]byte
)

func SaveGlobalLedgerIdentityData(ledgerID *IdentityData) {
	ledgerIDSingleton = ledgerID
	ledgerIDHashSingleton = ledgerIDSingleton.Hash()
}

func GetGlobalLedgerIdentity() *IdentityData {
	return ledgerIDSingleton
}
