package ledger

// BuildSlotLocalConsumptionLibrary enables the alternative accounting rule in
// a compiled library. It is deliberately not registered in Upgrades: choosing
// a public activation slot requires network agreement.
func BuildSlotLocalConsumptionLibrary(prevJSON []byte) ([]byte, error) {
	return buildUpgrade(prevJSON, "slot-local consumption coverage", "func constSlotLocalConsumptionCoverage : 0x01")
}
