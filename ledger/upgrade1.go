package ledger

func (lib *Library) upgrade1() {
	lib.upgrade1WithConstraints()
}

func (lib *Library) upgrade1WithConstraints() {
	addDelegationLock(lib)
}
