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
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/txutils"
	"golang.org/x/crypto/blake2b"
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

func (c *APIClient) GetChainOutputFromHeaviestState(chainID core.ChainID) (*core.OutputWithChainID, byte, error) {
	oData, err := c.GetChainOutputData(chainID)
	if err != nil {
		return nil, 0, err
	}
	return oData.ParseAsChainOutput()
}

func (c *APIClient) GetMilestoneDataFromHeaviestState(chainID core.ChainID) (*sequencer.MilestoneData, error) {
	o, _, err := c.GetChainOutputFromHeaviestState(chainID)
	if err != nil {
		return nil, err
	}
	if !o.ID.IsSequencerTransaction() {
		return nil, fmt.Errorf("not a sequencer milestone: %s", chainID.Short())
	}
	return sequencer.ParseMilestoneData(o.Output), nil
}

// GetOutputDataFromHeaviestState returns output data from the latest heaviest state, if it exists there
// Returns nil, nil if output does not exist
func (c *APIClient) GetOutputDataFromHeaviestState(oid *core.OutputID) ([]byte, error) {
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
	if res.Error.Error == api.ErrGetOutputNotFound {
		return nil, nil
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}

	oData, err := hex.DecodeString(res.OutputData)
	if err != nil {
		return nil, fmt.Errorf("can't decode output data: %v", err)
	}

	return oData, nil
}

func (c *APIClient) GetOutputDataWithInclusion(oid *core.OutputID) ([]byte, []api.InclusionDecoded, error) {
	path := fmt.Sprintf(api.PathGetOutputWithInclusion+"?id=%s", oid.StringHex())
	body, err := c.getBody(path)
	if err != nil {
		return nil, nil, err
	}

	var res api.OutputData
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, nil, err
	}
	if res.Error.Error == api.ErrGetOutputNotFound {
		return nil, nil, nil
	}
	if res.Error.Error != "" {
		return nil, nil, fmt.Errorf("from server: %s", res.Error.Error)
	}

	oData, err := hex.DecodeString(res.OutputData)
	if err != nil {
		return nil, nil, fmt.Errorf("can't decode output data: %v", err)
	}
	ret := make([]api.InclusionDecoded, len(res.Inclusion))
	for i := range ret {
		ret[i], err = res.Inclusion[i].Decode()
		if err != nil {
			return nil, nil, err
		}
	}
	return oData, ret, nil
}

const waitOutputFinalPollPeriod = 500 * time.Millisecond

// WaitOutputInTheHeaviestState return true once output is found in the latest heaviest branch.
// Polls node until success or timeout
func (c *APIClient) WaitOutputInTheHeaviestState(oid *core.OutputID, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var res []byte
	var err error
	for {
		if res, err = c.GetOutputDataFromHeaviestState(oid); err != nil {
			return err
		}
		if len(res) > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("WaitOutputInTheHeaviestState %s: timeout %v", oid.Short(), timeout)
		}
		time.Sleep(waitOutputFinalPollPeriod)
	}
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

func (c *APIClient) GetTransferableOutputs(account core.Accountable, ts core.LogicalTime, maxOutputs ...int) ([]*core.OutputWithID, uint64, error) {
	ret, err := c.GetAccountOutputs(account, func(o *core.Output) bool {
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
		return nil, 0, err
	}
	if len(ret) == 0 {
		return nil, 0, nil
	}
	maxOut := 256
	if len(maxOutputs) > 0 && maxOutputs[0] > 0 && maxOutputs[0] < 256 {
		maxOut = maxOutputs[0]
	}
	if len(ret) > maxOut {
		ret = ret[:maxOut]
	}
	sum := uint64(0)
	for _, o := range ret {
		sum += o.Output.Amount()
	}
	return ret, sum, nil
}

func (c *APIClient) CompactED25519Outputs(walletPrivateKey ed25519.PrivateKey, tagAlongSeqID *core.ChainID, tagAlongFee uint64, maxOutputs ...int) (*transaction.TransactionContext, error) {
	walletAccount := core.AddressED25519FromPrivateKey(walletPrivateKey)

	nowisTs := core.LogicalTimeNow()
	inTotal := uint64(0)

	walletOutputs, inTotal, err := c.GetTransferableOutputs(walletAccount, nowisTs, maxOutputs...)
	if len(walletOutputs) <= 1 {
		return nil, nil
	}
	if inTotal < tagAlongFee {
		return nil, fmt.Errorf("non enough balance for fees")
	}
	txBytes, err := MakeTransferTransaction(MakeTransferTransactionParams{
		Inputs:        walletOutputs,
		Target:        walletAccount,
		Amount:        inTotal - tagAlongFee,
		PrivateKey:    walletPrivateKey,
		TagAlongSeqID: tagAlongSeqID,
		TagAlongFee:   tagAlongFee,
		Timestamp:     nowisTs,
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
	MaxOutputs       int
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

	walletOutputs, _, err := c.GetTransferableOutputs(walletAccount, nowisTs, par.MaxOutputs)

	txBytes, err := MakeTransferTransaction(MakeTransferTransactionParams{
		Inputs:        walletOutputs,
		Target:        par.Target,
		Amount:        par.Amount,
		PrivateKey:    par.WalletPrivateKey,
		TagAlongSeqID: par.TagAlongSeqID,
		TagAlongFee:   par.TagAlongFee,
		Timestamp:     nowisTs,
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

func (c *APIClient) MakeChainOrigin(par TransferFromED25519WalletParams) (*transaction.TransactionContext, core.ChainID, error) {
	if par.Amount < minimumAmount {
		return nil, core.NilChainID, fmt.Errorf("minimum transfer amount is %d", minimumAmount)
	}
	if par.Amount > 0 && par.TagAlongSeqID == nil {
		return nil, [32]byte{}, fmt.Errorf("tag-along sequencer not specified")
	}

	walletAccount := core.AddressED25519FromPrivateKey(par.WalletPrivateKey)

	ts := core.LogicalTimeNow()
	inps, totalInputs, err := c.GetTransferableOutputs(walletAccount, ts)
	if err != nil {
		return nil, [32]byte{}, err
	}
	if totalInputs < par.Amount+par.TagAlongFee {
		return nil, [32]byte{}, fmt.Errorf("not enough source balance %s", util.GoThousands(totalInputs))
	}

	totalInputs = 0
	inps = util.FilterSlice(inps, func(o *core.OutputWithID) bool {
		if totalInputs < par.Amount+par.TagAlongFee {
			totalInputs += o.Output.Amount()
			return true
		}
		return false
	})

	txb := txbuilder.NewTransactionBuilder()
	_, ts1, err := txb.ConsumeOutputs(inps...)
	if err != nil {
		return nil, [32]byte{}, err
	}
	ts = core.MaxLogicalTime(ts1.AddTimeTicks(core.TransactionTimePaceInTicks), ts)

	err = txb.PutStandardInputUnlocks(len(inps))
	glb.AssertNoError(err)

	chainOut := core.NewOutput(func(o *core.Output) {
		_, _ = o.WithAmount(par.Amount).
			WithLock(par.Target).
			PushConstraint(core.NewChainOrigin().Bytes())
	})
	_, err = txb.ProduceOutput(chainOut)
	glb.AssertNoError(err)

	if par.TagAlongFee > 0 {
		tagAlongFeeOut := core.NewOutput(func(o *core.Output) {
			o.WithAmount(par.TagAlongFee).
				WithLock(core.ChainLockFromChainID(*par.TagAlongSeqID))
		})
		if _, err = txb.ProduceOutput(tagAlongFeeOut); err != nil {
			return nil, [32]byte{}, err
		}
	}

	if totalInputs > par.Amount+par.TagAlongFee {
		remainder := core.NewOutput(func(o *core.Output) {
			o.WithAmount(totalInputs - par.Amount - par.TagAlongFee).
				WithLock(walletAccount)
		})
		if _, err = txb.ProduceOutput(remainder); err != nil {
			return nil, [32]byte{}, err
		}
	}
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(par.WalletPrivateKey)

	txBytes := txb.TransactionData.Bytes()

	txCtx, err := transaction.ContextFromTransferableBytes(txBytes, transaction.PickOutputFromListFunc(inps))
	if err != nil {
		return nil, [32]byte{}, err
	}
	if err = c.SubmitTransaction(txBytes); err != nil {
		return nil, [32]byte{}, err
	}

	oChain, err := transaction.OutputWithIDFromTransactionBytes(txBytes, 0)
	if err != nil {
		return nil, [32]byte{}, err
	}

	chainID := blake2b.Sum256(oChain.ID[:])
	return txCtx, chainID, err
}

type MakeTransferTransactionParams struct {
	Inputs        []*core.OutputWithID
	Target        core.Lock
	Amount        uint64
	Remainder     core.Lock
	PrivateKey    ed25519.PrivateKey
	TagAlongSeqID *core.ChainID
	TagAlongFee   uint64
	Timestamp     core.LogicalTime
}

func MakeTransferTransaction(par MakeTransferTransactionParams) ([]byte, error) {
	if par.Amount < minimumAmount {
		return nil, fmt.Errorf("minimum transfer amount is %d", minimumAmount)
	}
	txb := txbuilder.NewTransactionBuilder()
	inTotal, inTs, err := txb.ConsumeOutputs(par.Inputs...)
	if err != nil {
		return nil, err
	}
	if !core.ValidTimePace(inTs, par.Timestamp) {
		return nil, fmt.Errorf("inconsistency: wrong time constraints")
	}
	if inTotal < par.Amount+par.TagAlongFee {
		return nil, fmt.Errorf("not enough balance")
	}

	for i := range par.Inputs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			_ = txb.PutUnlockReference(byte(i), core.ConstraintIndexLock, 0)
		}
	}

	mainOut := core.NewOutput(func(o *core.Output) {
		o.WithAmount(par.Amount).
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
		feeOut := core.NewOutput(func(o *core.Output) {
			o.WithAmount(par.TagAlongFee).
				WithLock(core.ChainLockFromChainID(*par.TagAlongSeqID))
		})
		if _, err = txb.ProduceOutput(feeOut); err != nil {
			return nil, err
		}
	}
	// produce remainder if needed
	if inTotal > par.Amount+par.TagAlongFee {
		remainderLock := par.Remainder
		if remainderLock == nil {
			remainderLock = core.AddressED25519FromPrivateKey(par.PrivateKey)
		}
		remainderOut := core.NewOutput(func(o *core.Output) {
			o.WithAmount(inTotal - par.Amount - par.TagAlongFee).
				WithLock(remainderLock)
		})
		if _, err = txb.ProduceOutput(remainderOut); err != nil {
			return nil, err
		}
	}

	txb.TransactionData.Timestamp = par.Timestamp
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(par.PrivateKey)

	return txb.TransactionData.Bytes(), nil
}
