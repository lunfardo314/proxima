package client

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/util/txutils"
)

const apiDefaultClientTimeout = 3 * time.Second

type APIClient struct {
	c      http.Client
	prefix string
}

func New(serverURL string, timeout ...time.Duration) *APIClient {
	var to time.Duration
	if len(timeout) > 0 {
		to = timeout[0]
	} else {
		to = apiDefaultClientTimeout
	}
	return &APIClient{
		c:      http.Client{Timeout: to},
		prefix: serverURL,
	}
}

// getAccountOutputs fetches all outputs of the account
func (c *APIClient) getAccountOutputs(accountable core.Accountable) ([]*core.OutputDataWithID, error) {
	path := fmt.Sprintf(api.PathGetAccountOutputs+"?accountable=%s", accountable.String())
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}

	var res api.OutputList
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, err
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}

	ret := make([]*core.OutputDataWithID, 0, len(res.Outputs))

	for idStr, dataStr := range res.Outputs {
		id, err := core.OutputIDFromHexString(idStr)
		if err != nil {
			return nil, fmt.Errorf("wrong output ID data from server: %s", idStr)
		}
		oData, err := hex.DecodeString(dataStr)
		if err != nil {
			return nil, fmt.Errorf("wrong output data from server: %s", dataStr)
		}
		ret = append(ret, &core.OutputDataWithID{
			ID:         id,
			OutputData: oData,
		})
	}

	sort.Slice(ret, func(i, j int) bool {
		return bytes.Compare(ret[i].ID[:], ret[j].ID[:]) < 0
	})
	return ret, nil
}

func (c *APIClient) GetChainOutputData(chainID core.ChainID) (*core.OutputDataWithID, error) {
	path := fmt.Sprintf(api.PathGetChainOutput+"?chainid=%s", chainID.StringHex())
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}

	var res api.ChainOutput
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, err
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}

	oid, err := core.OutputIDFromHexString(res.OutputID)
	if err != nil {
		return nil, fmt.Errorf("wrong output ID data from server: %s", res.OutputID)
	}
	oData, err := hex.DecodeString(res.OutputData)
	if err != nil {
		return nil, fmt.Errorf("wrong output data from server: %s", res.OutputData)
	}

	return &core.OutputDataWithID{
		ID:         oid,
		OutputData: oData,
	}, nil
}

func (c *APIClient) GetChainOutput(chainID core.ChainID) (*core.OutputWithChainID, byte, error) {
	oData, err := c.GetChainOutputData(chainID)
	if err != nil {
		return nil, 0, err
	}
	return oData.ParseAsChainOutput()
}

func (c *APIClient) GetMilestoneData(chainID core.ChainID) (*sequencer.MilestoneData, error) {
	o, _, err := c.GetChainOutput(chainID)
	if err != nil {
		return nil, err
	}
	if !o.ID.IsSequencerTransaction() {
		return nil, fmt.Errorf("not a sequencer milestone: %s", chainID.Short())
	}
	return sequencer.ParseMilestoneData(o.Output), nil
}

func (c *APIClient) GetOutputData(oid *core.OutputID) ([]byte, error) {
	path := fmt.Sprintf(api.PathGetOutput+"?id=%s", oid.StringHex())
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}

	var res api.OutputData
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, err
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}

	oData, err := hex.DecodeString(res.OutputData)
	if err != nil {
		return nil, fmt.Errorf("wrong output data from server: %s", res.OutputData)
	}

	return oData, nil
}

func (c *APIClient) SubmitTransaction(txBytes []byte, nowait ...bool) error {
	url := c.prefix + api.PathSubmitTransactionWait
	if len(nowait) > 0 && nowait[0] {
		url = c.prefix + api.PathSubmitTransactionNowait
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(txBytes))
	if err != nil {
		return err
	}
	resp, err := c.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var res api.Error
	err = json.Unmarshal(body, &res)
	if err != nil {
		return err
	}
	if res.Error != "" {
		return fmt.Errorf("from server: %s", res.Error)
	}
	return nil
}

func (c *APIClient) GetAccountOutputs(account core.Accountable, filter ...func(o *core.Output) bool) ([]*core.OutputWithID, error) {
	filterFun := func(o *core.Output) bool { return true }
	if len(filter) > 0 {
		filterFun = filter[0]
	}
	oData, err := c.getAccountOutputs(account)
	if err != nil {
		return nil, err
	}

	outs, err := txutils.ParseAndSortOutputData(oData, filterFun, true)
	if err != nil {
		return nil, err
	}
	return outs, nil
}

func (c *APIClient) GetTransferableBalance(account core.Accountable, ts core.LogicalTime) (uint64, int, error) {
	walletOutputs, err := c.GetAccountOutputs(account, func(o *core.Output) bool {
		// filter out chain outputs controlled by the wallet
		_, idx := o.ChainConstraint()
		if idx != 0xff {
			return false
		}
		if !o.Lock().UnlockableWith(account.AccountID(), ts) {
			return false
		}
		return true
	})
	if err != nil {
		return 0, 0, err
	}
	if len(walletOutputs) == 0 {
		return 0, 0, nil
	}
	if len(walletOutputs) > 256 {
		walletOutputs = walletOutputs[:256]
	}
	sum := uint64(0)
	for _, o := range walletOutputs {
		sum += o.Output.Amount()
	}
	return sum, len(walletOutputs), nil
}

func (c *APIClient) CompactED25519Outputs(walletPrivateKey ed25519.PrivateKey, tagAlongSeqID *core.ChainID, tagAlongFee uint64) (*transaction.TransactionContext, error) {
	walletAccount := core.AddressED25519FromPrivateKey(walletPrivateKey)

	nowisTs := core.LogicalTimeNow()
	inTotal := uint64(0)
	walletOutputs, err := c.GetAccountOutputs(walletAccount, func(o *core.Output) bool {
		// filter out chain outputs controlled by the wallet
		_, idx := o.ChainConstraint()
		if idx != 0xff {
			return false
		}
		if !o.Lock().UnlockableWith(walletAccount.AccountID(), nowisTs) {
			return false
		}
		inTotal += o.Amount()
		return true
	})
	if err != nil {
		return nil, err
	}
	if len(walletOutputs) <= 1 {
		return nil, nil
	}
	if len(walletOutputs) > 256 {
		walletOutputs = walletOutputs[:256]
	}
	if inTotal < tagAlongFee {
		return nil, fmt.Errorf("non enough balance for fees")
	}
	txBytes, err := makeTransferTransaction(makeTransferTransactionParams{
		inputs:           walletOutputs,
		target:           walletAccount,
		amount:           inTotal - tagAlongFee,
		walletPrivateKey: walletPrivateKey,
		tagAlongSeqID:    tagAlongSeqID,
		tagAlongFee:      tagAlongFee,
		nowisTs:          nowisTs,
	})
	if err != nil {
		return nil, err
	}

	txCtx, err := transaction.ContextFromTransferableBytes(txBytes, transaction.PickOutputFromListFunc(walletOutputs))
	if err != nil {
		return nil, err
	}

	err = c.SubmitTransaction(txBytes)
	return txCtx, err
}

type TransferFromED25519WalletParams struct {
	WalletPrivateKey ed25519.PrivateKey
	TagAlongSeqID    *core.ChainID
	TagAlongFee      uint64 // 0 means no fee output will be produced
	Amount           uint64
	Target           core.Lock
}

const (
	minimumAmount = uint64(500)
)

func (c *APIClient) TransferFromED25519Wallet(par TransferFromED25519WalletParams) (*transaction.TransactionContext, error) {
	if par.Amount < minimumAmount {
		return nil, fmt.Errorf("minimum transfer amount is %d", minimumAmount)
	}
	walletAccount := core.AddressED25519FromPrivateKey(par.WalletPrivateKey)
	nowisTs := core.LogicalTimeNow()
	walletOutputs, err := c.GetAccountOutputs(walletAccount, func(o *core.Output) bool {
		// filter out chain outputs controlled by the wallet
		_, idx := o.ChainConstraint()
		if idx != 0xff {
			return false
		}
		return o.Lock().UnlockableWith(walletAccount.AccountID(), nowisTs)
	})
	if err != nil {
		return nil, err
	}
	if len(walletOutputs) == 0 || len(walletOutputs) > 256 {
		return nil, fmt.Errorf("cannot transfer this amount from the wallet")
	}

	txBytes, err := makeTransferTransaction(makeTransferTransactionParams{
		inputs:           walletOutputs,
		target:           par.Target,
		amount:           par.Amount,
		walletPrivateKey: par.WalletPrivateKey,
		tagAlongSeqID:    par.TagAlongSeqID,
		tagAlongFee:      par.TagAlongFee,
		nowisTs:          nowisTs,
	})
	if err != nil {
		return nil, err
	}
	txCtx, err := transaction.ContextFromTransferableBytes(txBytes, transaction.PickOutputFromListFunc(walletOutputs))
	if err != nil {
		return nil, err
	}
	err = c.SubmitTransaction(txBytes)
	return txCtx, err
}

func (c *APIClient) getBody(path string) ([]byte, error) {
	url := c.prefix + path
	resp, err := c.c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

type makeTransferTransactionParams struct {
	inputs           []*core.OutputWithID
	target           core.Lock
	amount           uint64
	remainder        core.Lock
	walletPrivateKey ed25519.PrivateKey
	tagAlongSeqID    *core.ChainID
	tagAlongFee      uint64
	nowisTs          core.LogicalTime
}

func makeTransferTransaction(par makeTransferTransactionParams) ([]byte, error) {
	if par.amount < minimumAmount {
		return nil, fmt.Errorf("minimum transfer amount is %d", minimumAmount)
	}
	txb := txbuilder.NewTransactionBuilder()
	inTotal, inTs, err := txb.ConsumeOutputs(par.inputs...)
	if err != nil {
		return nil, err
	}
	if !core.ValidTimePace(inTs, par.nowisTs) {
		return nil, fmt.Errorf("inconsistency: wrong time constraints")
	}
	if inTotal < par.amount+par.tagAlongFee {
		return nil, fmt.Errorf("not enough balance")
	}

	for i := range par.inputs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			_ = txb.PutUnlockReference(byte(i), core.ConstraintIndexLock, 0)
		}
	}

	mainOut := core.NewOutput(func(o *core.Output) {
		o.WithAmount(par.amount).
			WithLock(par.target)
	})
	if _, err = txb.ProduceOutput(mainOut); err != nil {
		return nil, err
	}
	// produce tag-along fee output, if needed
	if par.tagAlongFee > 0 {
		if par.tagAlongSeqID == nil {
			return nil, fmt.Errorf("tag-along sequencer not specified")
		}
		feeOut := core.NewOutput(func(o *core.Output) {
			o.WithAmount(par.tagAlongFee).
				WithLock(core.ChainLockFromChainID(*par.tagAlongSeqID))
		})
		if _, err = txb.ProduceOutput(feeOut); err != nil {
			return nil, err
		}
	}
	// produce remainder if needed
	if inTotal > par.amount+par.tagAlongFee {
		remainderLock := par.remainder
		if remainderLock == nil {
			remainderLock = core.AddressED25519FromPrivateKey(par.walletPrivateKey)
		}
		remainderOut := core.NewOutput(func(o *core.Output) {
			o.WithAmount(inTotal - par.amount - par.tagAlongFee).
				WithLock(remainderLock)
		})
		if _, err = txb.ProduceOutput(remainderOut); err != nil {
			return nil, err
		}
	}

	txb.TransactionData.Timestamp = par.nowisTs
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(par.walletPrivateKey)

	return txb.TransactionData.Bytes(), nil
}
