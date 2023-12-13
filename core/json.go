package core

import (
	"encoding/json"
)

func (txid *TransactionID) MarshalJSON() ([]byte, error) {
	return []byte("\"" + txid.StringHex() + "\""), nil
}

func (txid *TransactionID) UnmarshalJSON(hexStrData []byte) error {
	var hexStr string
	err := json.Unmarshal(hexStrData, &hexStr)
	if err != nil {
		return err
	}
	ret, err := TransactionIDFromHexString(hexStr)
	if err != nil {
		return err
	}
	*txid = ret
	return nil
}

func (id *ChainID) MarshalJSON() ([]byte, error) {
	return []byte("\"" + id.StringHex() + "\""), nil
}

func (id *ChainID) UnmarshalJSON(hexStrData []byte) error {
	var hexStr string
	err := json.Unmarshal(hexStrData, &hexStr)
	if err != nil {
		return err
	}
	ret, err := ChainIDFromHexString(hexStr)
	if err != nil {
		return err
	}
	*id = ret
	return nil
}
