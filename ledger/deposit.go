package ledger

// TODO propper calculation of the storage deposit

func MinimumStorageDeposit(o *Output, extraWeight uint32) uint64 {
	if _, isStem := o.StemLock(); isStem {
		return 0
	}
	return uint64(len(o.Bytes()))
}
