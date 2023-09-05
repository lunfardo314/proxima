package txbuilder

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/core"
	transaction2 "github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
)

// MakeTransactionSequence make sequence of transactions, which moves all tokens from firstUTXO to the same adddress
// again and again. Timestamps are auto automatically adjusted according to tx pace
func MakeTransactionSequence(howLong int, firstUTXO *core.OutputWithID, privK ed25519.PrivateKey) ([][]byte, error) {
	pubK := privK.Public().(ed25519.PublicKey)
	addr := core.AddressED25519FromPublicKey(pubK)

	ret := make([][]byte, howLong)
	td := &TransferData{
		SenderPrivateKey: privK,
		SenderPublicKey:  pubK,
		SourceAccount:    addr,
		Inputs:           []*core.OutputWithID{firstUTXO},
		Timestamp:        core.NilLogicalTime,
		Lock:             addr,
		Amount:           firstUTXO.Output.Amount(),
	}
	for i := range ret {
		txBytes, err := MakeSimpleTransferTransaction(td)
		if err != nil {
			return nil, err
		}
		ret[i] = txBytes
		tx, err := transaction2.FromBytes(txBytes)
		if err != nil {
			return nil, err
		}
		outs := tx.ProducedOutputs()
		util.Assertf(len(outs) == 1, "inconsistency")

		td.Inputs = []*core.OutputWithID{outs[0]}
		td.Amount = outs[0].Output.Amount()
	}
	return ret, nil
}

func MakeTransactionSequences(howLong int, firstUTXO []*core.OutputWithID, privK []ed25519.PrivateKey) ([][][]byte, error) {
	util.Assertf(len(firstUTXO) == len(privK), "len(firstUTXO)==len(privK)")
	ret := make([][][]byte, len(firstUTXO))
	var err error
	for i := range ret {
		ret[i], err = MakeTransactionSequence(howLong, firstUTXO[i], privK[i])
		if err != nil {
			return nil, err
		}
	}
	return ret, nil
}
