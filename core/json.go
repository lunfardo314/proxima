package core

func (txid *TransactionID) MarshalJSON() ([]byte, error) {
	return []byte(txid.StringHex()), nil
}

func (txid *TransactionID) UnmarshalJSON(hexStrData []byte) error {
	ret, err := TransactionIDFromHexString(string(hexStrData))
	if err != nil {
		return err
	}
	*txid = ret
	return nil
}

func (id *ChainID) MarshalJSON() ([]byte, error) {
	return []byte(id.StringHex()), nil
}

func (id *ChainID) UnmarshalJSON(hexStrData []byte) error {
	ret, err := ChainIDFromHexString(string(hexStrData))
	if err != nil {
		return err
	}
	*id = ret
	return nil
}
