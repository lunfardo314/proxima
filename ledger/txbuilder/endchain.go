package txbuilder

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
)

type EndChainParams struct {
	// transaction timestamp. Not adjusted
	Timestamp base.LedgerTime
	// chain output data
	ChainIn *ledger.OutputWithChainID
	// controlling private key
	PrivateKey ed25519.PrivateKey
	// tag-along sequencer and fee amount
	TagAlongSeqID base.ChainID
	TagAlongFee   uint64 // 0 means no fee output will be produced
}

func MakeEndChainTransaction(par EndChainParams) (*transaction.Transaction, error) {
	txb := New()

	consumedIndex, err := txb.ConsumeOutput(par.ChainIn.Output, par.ChainIn.ID)
	util.AssertNoError(err)

	feeAmount := par.TagAlongFee

	outNonChain := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(par.ChainIn.Output.TokenBalance() - feeAmount)).
			WithLock(ledger.SigLockFromED25519PrivateKey(par.PrivateKey))
	})
	_, err = txb.ProduceOutput(outNonChain)
	util.AssertNoError(err)

	if feeAmount > 0 {
		tagAlongFeeOut := ledger.NewTagAlongOutput(feeAmount, par.TagAlongSeqID, base.HolderID(ledger.SigLockFromED25519PrivateKey(par.PrivateKey)))
		if _, err = txb.ProduceOutput(tagAlongFeeOut); err != nil {
			return nil, err
		}
	}

	// additional byte 0xff is added to unlock parameters to satisfy 'master unlock' condition of the delegation lock
	txb.PutSignatureUnlock(consumedIndex, ledger.DelegationUnlockedByMaster)
	txb.PutUnlockParams(consumedIndex, ledger.ConstraintIndexChain, ledger.FinishChainUnlockParams)

	// finalize the transaction
	txb.TransactionData.Timestamp = par.Timestamp
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(par.PrivateKey)

	tx, err := txb.Transaction()
	if err != nil {
		return nil, err
	}
	return tx, nil
}
