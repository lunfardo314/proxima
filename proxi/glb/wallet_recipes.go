package glb

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

// Legacy high-level recipe helpers moved out of api/client into proxi
// glb. These were originally on *client.APIClient; they remained
// proxi-specific wallet sugar and are scheduled for replacement by
// the wasm-style txbuildercore + helpers pipeline during Phase 1 of
// the refactor (see claude/proxi_txbuildercore.md). Kept here
// temporarily so per-site Phase 1 work can land incrementally.

const minimumTransferAmount = uint64(1000)

// TransferFromED25519WalletParams is the parameter object for
// TransferFromED25519Wallet and MakeChainOrigin.
type TransferFromED25519WalletParams struct {
	WalletPrivateKey ed25519.PrivateKey
	TagAlongSeqID    *base.ChainID
	TagAlongFee      uint64 // 0 means no fee output will be produced
	Amount           uint64
	Target           ledger.Lock
	MaxOutputs       int
	// DelegationParams, if non-nil, attaches the delegationParams
	// constraint at index 6 on the chain origin output, opting the
	// chain into accepting delegations (Phase 5 of
	// claude/delegation_epoch_params.md). Only consulted by
	// MakeChainOrigin.
	DelegationParams *ledger.DelegationParams
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
	res, err := c.GetOutputs(walletAccount.ControllerID(), client.GetOutputsParams{
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

// MakeChainOrigin creates a chain-origin output and submits the
// origin transaction. Returns the parsed tx plus the new chain ID.
func MakeChainOrigin(par TransferFromED25519WalletParams) (*transaction.Transaction, base.ChainID, error) {
	if par.Amount < minimumTransferAmount {
		return nil, base.NilChainID, fmt.Errorf("minimum transfer amount is %d", minimumTransferAmount)
	}
	c := GetClient()
	walletAccount := ledger.SigLockFromED25519PrivateKey(par.WalletPrivateKey)

	ts := ledger.TimeNow()
	inps, _, totalInputs, err := c.GetTransferableOutputs(walletAccount)
	if err != nil {
		return nil, [32]byte{}, err
	}
	if totalInputs < par.Amount+par.TagAlongFee {
		return nil, [32]byte{}, fmt.Errorf("not enough source balance %s", util.Th(totalInputs))
	}

	totalInputs = 0
	inps = util.PurgeSlice(inps, func(o *ledger.OutputWithID) bool {
		if totalInputs < par.Amount+par.TagAlongFee {
			totalInputs += o.Output.TokenBalance()
			return true
		}
		return false
	})

	txb := txbuilder.New()
	_, ts1, err := txb.ConsumeOutputsNoUnlock(inps...)
	if err != nil {
		return nil, [32]byte{}, err
	}
	ts = base.MaximumTime(ts1.AddTicks(int(ledger.L(base.MaxSlot).TransactionPace)), ts)

	err = txb.PutStandardInputUnlocks(len(inps))
	util.AssertNoError(err)

	chainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(par.Amount).
			WithLock(par.Target).
			MustPushConstraint(ledger.NewChainOrigin(ts.Slot).Bytes())
		if par.DelegationParams != nil {
			// Attach the delegationParams constraint at its fixed index
			// (Phase 5 of claude/delegation_epoch_params.md). The chain
			// becomes a delegation target; immutability is enforced by
			// selfImmutableOnSuccessorIndex(6) on the constraint body.
			o.PutConstraint(par.DelegationParams.Bytes(), ledger.ConstraintIndexDelegationParams)
		}
	})
	_, err = txb.ProduceOutput(chainOut)
	util.AssertNoError(err)

	if par.TagAlongFee > 0 {
		tagAlongFeeOut := ledger.NewTagAlongOutput(par.TagAlongFee, *par.TagAlongSeqID, base.HolderID(ledger.SigLockFromED25519PrivateKey(par.WalletPrivateKey)))
		if _, err = txb.ProduceOutput(tagAlongFeeOut); err != nil {
			return nil, [32]byte{}, err
		}
	}

	if totalInputs > par.Amount+par.TagAlongFee {
		remainder := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(totalInputs - par.Amount - par.TagAlongFee).
				WithLock(walletAccount)
		})
		if _, err = txb.ProduceOutput(remainder); err != nil {
			return nil, [32]byte{}, err
		}
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(par.WalletPrivateKey)

	txBytes := txb.Bytes()

	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil {
		return tx, [32]byte{}, err
	}
	err = tx.SetFullContext(tx.InputLoaderByIndex(transaction.PickOutputFromListFunc(inps)))
	if err != nil {
		return tx, [32]byte{}, err
	}
	err = c.SubmitTransaction(txBytes)
	if err != nil {
		return tx, [32]byte{}, err
	}
	oChain, err := transaction.OutputWithIDFromTransactionBytes(txBytes, 0)
	if err != nil {
		return nil, [32]byte{}, err
	}

	chainID := blake2b.Sum256(oChain.ID[:])
	return tx, chainID, err
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

