package glb

// DISABLED — legacy proxi wallet recipes (TransferFromED25519Wallet /
// MakeTransferTransaction / MakeSendOutputTransaction +
// TransferFromED25519WalletParams / MakeTransferTransactionParams).
//
// These were the sigLock-transfer + send-output sugar built on
// ledger/txbuilder + ledger.NewOutput + the ledger.L() singleton. The
// only reachable call sites were inside the faucet server
// (proxi/node_cmd/faucet_srv.go), which is itself disabled in
// lockstep below. The whole file is commented off rather than deleted
// so it can be revived alongside faucet_srv when the faucet is ported
// to the wasm-style txbuildercore pipeline. Until then, new sites
// should compose via txbuildercore + the wallet helpers and submit
// via glb.SubmitAndDisplay (canonical templates:
// proxi/node_cmd/{compact,send}.go).

/*
import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
)

const minimumTransferAmount = uint64(1000)

// TransferFromED25519WalletParams is the parameter object for
// TransferFromED25519Wallet.
type TransferFromED25519WalletParams struct {
	WalletPrivateKey ed25519.PrivateKey
	TagAlongSeqID    *base.ChainID
	TagAlongFee      uint64 // 0 means no fee output will be produced
	Amount           uint64
	Target           ledger.Lock
	MaxOutputs       int
}

// MakeTransferTransactionParams is the parameter object for
// MakeTransferTransaction.
type MakeTransferTransactionParams struct {
	Inputs        []*ledger.OutputWithID
	Target        ledger.Lock
	Amount        uint64
	Remainder     ledger.Lock
	PrivateKey    ed25519.PrivateKey
	TagAlongSeqID *base.ChainID
	TagAlongFee   uint64
	Timestamp     base.LedgerTime
}

// MakeTransferTransaction builds and signs a sigLock transfer
// transaction (with optional tag-along + remainder). Pure compose
// helper — does NOT submit.
func MakeTransferTransaction(par MakeTransferTransactionParams) ([]byte, error) {
	if par.Amount < minimumTransferAmount {
		return nil, fmt.Errorf("minimum transfer amount is %d", minimumTransferAmount)
	}
	txb := txbuilder.New()
	inTotal, inTs, err := txb.ConsumeOutputsNoUnlock(par.Inputs...)
	if err != nil {
		return nil, err
	}
	if !ledger.ValidTransactionPace(inTs, par.Timestamp) {
		return nil, fmt.Errorf("inconsistency: wrong time constraints")
	}
	if inTotal < par.Amount+par.TagAlongFee {
		return nil, fmt.Errorf("not enough balance")
	}

	for i := range par.Inputs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			_ = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
		}
	}

	mainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(par.Amount).
			WithLock(par.Target)
	})
	if _, err = txb.ProduceOutput(mainOut); err != nil {
		return nil, err
	}
	// produce tag-along fee output, if needed
	if par.TagAlongFee > 0 {
		if par.TagAlongSeqID == nil {
			return nil, fmt.Errorf("tag-along sequencer not specified")
		}
		tagAlongOut := ledger.NewTagAlongOutput(par.TagAlongFee, *par.TagAlongSeqID, base.HolderID(ledger.SigLockFromED25519PrivateKey(par.PrivateKey)))
		if _, err = txb.ProduceOutput(tagAlongOut); err != nil {
			return nil, err
		}
	}
	// produce remainder if needed
	if inTotal > par.Amount+par.TagAlongFee {
		remainderLock := par.Remainder
		if remainderLock == nil {
			remainderLock = ledger.SigLockFromED25519PrivateKey(par.PrivateKey)
		}
		remainderOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(inTotal - par.Amount - par.TagAlongFee).
				WithLock(remainderLock)
		})
		if _, err = txb.ProduceOutput(remainderOut); err != nil {
			return nil, err
		}
	}

	txb.SetTimestamp(par.Timestamp)
	txb.ComputeInputCommitment()
	txb.SignED25519(par.PrivateKey)

	txBytes, _, txString, err := txb.BytesWithValidation()

	if err != nil {
		err = fmt.Errorf("%v\n------ failing transaction -------\n%s", err, txString)
	}

	return txBytes, err
}

// TransferFromED25519Wallet runs a complete sig-lock transfer flow:
// fetches the wallet's available outputs, builds the tx via
// MakeTransferTransaction, and submits via the (legacy) client.SubmitTransaction.
func TransferFromED25519Wallet(par TransferFromED25519WalletParams) (*transaction.Transaction, error) {
	if par.Amount < minimumTransferAmount {
		return nil, fmt.Errorf("minimum transfer amount is %d", minimumTransferAmount)
	}
	c := GetClient()
	walletAccount := ledger.SigLockFromED25519PrivateKey(par.WalletPrivateKey)
	needed := par.Amount + par.TagAlongFee
	res, err := c.GetOutputsForControllerID(walletAccount.ControllerID(), client.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   client.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderDesc,
		ForAmount: needed,
	})
	if err != nil {
		return nil, err
	}
	if res.AvailableAmount < needed {
		return nil, fmt.Errorf("not enough tokens: have %d, need %d", res.AvailableAmount, needed)
	}
	walletOutputs := res.Outputs
	txBytes, err := MakeTransferTransaction(MakeTransferTransactionParams{
		Inputs:        walletOutputs,
		Target:        par.Target,
		Amount:        par.Amount,
		PrivateKey:    par.WalletPrivateKey,
		TagAlongSeqID: par.TagAlongSeqID,
		TagAlongFee:   par.TagAlongFee,
		Timestamp:     ledger.TimeNow(),
	})
	if err != nil {
		return nil, err
	}
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil {
		return tx, err
	}
	err = tx.SetFullContext(tx.InputLoaderByIndex(transaction.PickOutputFromListFunc(walletOutputs)))
	if err != nil {
		return tx, err
	}
	err = c.SubmitTransaction(txBytes)
	return tx, err
}

// MakeSendOutputTransaction builds a transaction that produces a
// given output `o` plus a remainder back to the wallet, using all
// the wallet's transferable outputs as inputs. Pure compose helper
// — does NOT submit.
func MakeSendOutputTransaction(o *ledger.Output, privateKey ed25519.PrivateKey, ts base.LedgerTime) ([]byte, base.TransactionID, string, error) {
	c := GetClient()
	account := ledger.SigLockFromED25519PrivateKey(privateKey)
	walletOutputs, _, amountInWallet, err := c.GetTransferableOutputs(account, 255)
	if err != nil {
		return nil, base.TransactionID{}, "", err
	}
	if len(walletOutputs) == 0 {
		return nil, base.TransactionID{}, "", fmt.Errorf("wallet has no outputs to create transaction")
	}
	bal := o.TokenBalance()
	if amountInWallet < bal {
		return nil, base.TransactionID{}, "", fmt.Errorf("not enough balance: have %d, need %d", amountInWallet, bal)
	}
	txb := txbuilder.New()
	for _, out := range walletOutputs {
		idx, err := txb.ConsumeOutput(out.Output, out.ID)
		if err != nil {
			return nil, base.TransactionID{}, "", err
		}
		if idx == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0)
			if err != nil {
				return nil, base.TransactionID{}, "", err
			}
		}
	}
	if amountInWallet > bal {
		// remainder
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(amountInWallet - bal)
			o.WithLock(account)
		}))
		if err != nil {
			return nil, base.TransactionID{}, "", err
		}
	}
	_, err = txb.ProduceOutput(o)
	if err != nil {
		return nil, base.TransactionID{}, "", err
	}

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(privateKey)

	txBytes, txid, txString, err := txb.BytesWithValidation()
	if err != nil {
		return nil, base.TransactionID{}, txString, err
	}
	return txBytes, txid, txString, nil
}
*/
