package genesis

// not thread-safe. Set once upon startup, later read-only

var (
	ledgerIDSingleton     *LedgerIdentityData
	ledgerIDHashSingleton [32]byte
)

func SaveGlobalLedgerIdentityData(ledgerID *LedgerIdentityData) {
	ledgerIDSingleton = ledgerID
	ledgerIDHashSingleton = ledgerIDSingleton.Hash()
}

func GetGlobalLedgerIdentity() *LedgerIdentityData {
	return ledgerIDSingleton
}
