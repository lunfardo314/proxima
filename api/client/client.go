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
	"github.com/lunfardo314/proxima/proxi/console"
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

func (c *APIClient) GetAccountOutputs(accountable core.Accountable) ([]*core.OutputDataWithID, error) {
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

func (c *APIClient) SubmitTransaction(txBytes []byte) error {
	url := c.prefix + api.PathSubmitTransaction
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

func (c *APIClient) CompactED25519Outputs(walletPrivateKey ed25519.PrivateKey, tagAlongSeqID *core.ChainID, tagAlongFee uint64) (*transaction.TransactionContext, error) {
	walletAccount := core.AddressED25519FromPrivateKey(walletPrivateKey)
	oData, err := c.GetAccountOutputs(walletAccount)
	if err != nil {
		return nil, err
	}

	nowisTs := core.LogicalTimeNow()
	walletOutputs, err := txutils.ParseAndSortOutputData(oData, func(o *core.Output) bool {
		// filter out chain outputs controlled by the wallet
		_, idx := o.ChainConstraint()
		if idx != 0xff {
			return false
		}
		return o.Lock().UnlockableWith(walletAccount.AccountID(), nowisTs)
	}, true)
	if err != nil {
		return nil, err
	}
	if len(walletOutputs) <= 1 {
		return nil, nil
	}
	if len(walletOutputs) > 256 {
		walletOutputs = walletOutputs[:256]
	}

	txb := txbuilder.NewTransactionBuilder()
	inTotal, inTs, err := txb.ConsumeOutputs(walletOutputs...)
	if err != nil {
		return nil, err
	}
	if !core.ValidTimePace(inTs, nowisTs) {
		return nil, fmt.Errorf("inconsistency: wrong time constraints")
	}
	if inTotal <= tagAlongFee {
		return nil, fmt.Errorf("not enough balance even for fees")
	}

	for i := range walletOutputs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			_ = txb.PutUnlockReference(byte(i), core.ConstraintIndexLock, 0)
		}
	}
	if tagAlongFee > 0 {
		console.Assertf(tagAlongSeqID != nil, "tagAlongSeqID != nil")
		feeOut := core.NewOutput(func(o *core.Output) {
			o.WithAmount(tagAlongFee).
				WithLock(core.ChainLockFromChainID(*tagAlongSeqID))
		})
		if _, err = txb.ProduceOutput(feeOut); err != nil {
			return nil, err
		}
	}

	remainderOut := core.NewOutput(func(o *core.Output) {
		o.WithAmount(inTotal - tagAlongFee).
			WithLock(walletAccount)
	})
	if _, err = txb.ProduceOutput(remainderOut); err != nil {
		return nil, err
	}

	txb.TransactionData.Timestamp = nowisTs
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(walletPrivateKey)
	txBytes := txb.TransactionData.Bytes()

	txCtx, err := transaction.ContextFromTransferableBytes(txBytes, transaction.PickOutputFromListFunc(walletOutputs))
	if err != nil {
		return nil, err
	}

	err = c.SubmitTransaction(txBytes)
	return txCtx, err
}

type TransferFromED25519WalletParams struct {
	WalletPrivateKey ed25519.PrivateKey
	TagAlongSeqID    core.ChainID
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
	oData, err := c.GetAccountOutputs(walletAccount)
	if err != nil {
		return nil, err
	}

	nowisTs := core.LogicalTimeNow()
	walletOutputs, _, _, err := txutils.ParseAndSortOutputDataUpToAmount(oData, par.Amount+par.TagAlongFee, func(o *core.Output) bool {
		// filter out chain outputs controlled by the wallet
		_, idx := o.ChainConstraint()
		if idx != 0xff {
			return false
		}
		return o.Lock().UnlockableWith(walletAccount.AccountID(), nowisTs)
	}, true)
	if err != nil {
		return nil, err
	}

	if len(walletOutputs) == 0 || len(walletOutputs) > 256 {
		return nil, fmt.Errorf("cannot transfer this amount from the wallet")
	}

	txb := txbuilder.NewTransactionBuilder()
	inTotal, inTs, err := txb.ConsumeOutputs(walletOutputs...)
	if err != nil {
		return nil, err
	}
	if !core.ValidTimePace(inTs, nowisTs) {
		return nil, fmt.Errorf("inconsistency: wrong time constraints")
	}

	for i := range walletOutputs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			_ = txb.PutUnlockReference(byte(i), core.ConstraintIndexLock, 0)
		}
	}
	if par.TagAlongFee > 0 {
		feeOut := core.NewOutput(func(o *core.Output) {
			o.WithAmount(par.TagAlongFee).
				WithLock(core.ChainLockFromChainID(par.TagAlongSeqID))
		})
		if _, err = txb.ProduceOutput(feeOut); err != nil {
			return nil, err
		}
	}

	remainderOut := core.NewOutput(func(o *core.Output) {
		o.WithAmount(inTotal - par.TagAlongFee - par.Amount).
			WithLock(walletAccount)
	})
	if _, err = txb.ProduceOutput(remainderOut); err != nil {
		return nil, err
	}

	txb.TransactionData.Timestamp = nowisTs
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(par.WalletPrivateKey)
	txBytes := txb.TransactionData.Bytes()

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
