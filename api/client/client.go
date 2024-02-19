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
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/txutils"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
)

const apiDefaultClientTimeout = 7 * time.Second

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

// GetLedgerID retrieves ledger ID from server
func (c *APIClient) GetLedgerID() (*ledger.IdentityData, error) {
	body, err := c.getBody(api.PathGetLedgerID)
	if err != nil {
		return nil, err
	}

	var res api.LedgerID
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, err
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("GetLedgerID: from server: %s", res.Error.Error)
	}

	idBin, err := hex.DecodeString(res.LedgerIDBytes)
	if err != nil {
		return nil, fmt.Errorf("GetLedgerID: error while decoding data: %w", err)
	}
	var ret *ledger.IdentityData
	err = common.CatchPanicOrError(func() error {
		ret = ledger.MustLedgerIdentityDataFromBytes(idBin)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("GetLedgerID: error while parsing received data: %w", err)
	}
	return ret, nil
}

// getAccountOutputs fetches all outputs of the account
func (c *APIClient) getAccountOutputs(accountable ledger.Accountable) ([]*ledger.OutputDataWithID, error) {
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

	ret := make([]*ledger.OutputDataWithID, 0, len(res.Outputs))

	for idStr, dataStr := range res.Outputs {
		id, err := ledger.OutputIDFromHexString(idStr)
		if err != nil {
			return nil, fmt.Errorf("wrong output ID data from server: %s", idStr)
		}
		oData, err := hex.DecodeString(dataStr)
		if err != nil {
			return nil, fmt.Errorf("wrong output data from server: %s", dataStr)
		}
		ret = append(ret, &ledger.OutputDataWithID{
			ID:         id,
			OutputData: oData,
		})
	}

	sort.Slice(ret, func(i, j int) bool {
		return bytes.Compare(ret[i].ID[:], ret[j].ID[:]) < 0
	})
	return ret, nil
}

func (c *APIClient) GetChainOutputData(chainID ledger.ChainID) (*ledger.OutputDataWithID, error) {
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

	oid, err := ledger.OutputIDFromHexString(res.OutputID)
	if err != nil {
		return nil, fmt.Errorf("wrong output ID data from server: %s", res.OutputID)
	}
	oData, err := hex.DecodeString(res.OutputData)
	if err != nil {
		return nil, fmt.Errorf("wrong output data from server: %s", res.OutputData)
	}

	return &ledger.OutputDataWithID{
		ID:         oid,
		OutputData: oData,
	}, nil
}

func (c *APIClient) GetChainOutputFromHeaviestState(chainID ledger.ChainID) (*ledger.OutputWithChainID, byte, error) {
	oData, err := c.GetChainOutputData(chainID)
	if err != nil {
		return nil, 0, err
	}
	return oData.ParseAsChainOutput()
}

func (c *APIClient) GetMilestoneDataFromHeaviestState(chainID ledger.ChainID) (*ledger.MilestoneData, error) {
	o, _, err := c.GetChainOutputFromHeaviestState(chainID)
	if err != nil {
		return nil, err
	}
	if !o.ID.IsSequencerTransaction() {
		return nil, fmt.Errorf("not a sequencer milestone: %s", chainID.StringShort())
	}
	return ledger.ParseMilestoneData(o.Output), nil
}

// GetOutputDataFromHeaviestState returns output data from the latest heaviest state, if it exists there
// Returns nil, nil if output does not exist
func (c *APIClient) GetOutputDataFromHeaviestState(oid *ledger.OutputID) ([]byte, error) {
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

func (c *APIClient) GetOutputInclusion(oid *ledger.OutputID) ([]api.InclusionData, error) {
	path := fmt.Sprintf(api.PathGetOutputInclusion+"?id=%s", oid.StringHex())
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

	ret := make([]api.InclusionData, len(res.Inclusion))
	for i := range ret {
		ret[i], err = res.Inclusion[i].Decode()
		if err != nil {
			return nil, err
		}
	}
	return ret, nil
}

const waitOutputFinalPollPeriod = 1 * time.Second

// WaitOutputInTheHeaviestState return true once output is found in the latest heaviest branch.
// Polls node until success or timeout
func (c *APIClient) WaitOutputInTheHeaviestState(oid *ledger.OutputID, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	if timeout == 0 {
		deadline = time.Now().Add(1 * time.Hour)
	}
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
			return fmt.Errorf("WaitOutputInTheHeaviestState %s: timeout %v", oid.StringShort(), timeout)
		}
		time.Sleep(waitOutputFinalPollPeriod)
	}
}

func (c *APIClient) SubmitTransaction(txBytes []byte) error {
	req, err := http.NewRequest(http.MethodPost, c.prefix+api.PathSubmitTransaction, bytes.NewBuffer(txBytes))
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

func (c *APIClient) GetAccountOutputs(account ledger.Accountable, filter ...func(o *ledger.Output) bool) ([]*ledger.OutputWithID, error) {
	filterFun := func(o *ledger.Output) bool { return true }
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

//func (c *APIClient) GetSyncInfo() (utangle_old.SyncInfo, error) {
//	body, err := c.getBody(api.PathGetSyncInfo)
//	if err != nil {
//		return utangle_old.SyncInfo{}, err
//	}
//
//	res := api.SyncInfo{PerSequencer: make(map[string]api.SequencerSyncInfo)}
//	err = json.Unmarshal(body, &res)
//	if err != nil {
//		return utangle_old.SyncInfo{}, err
//	}
//	ret := utangle_old.SyncInfo{
//		Synced:       res.Synced,
//		InSyncWindow: res.InSyncWindow,
//		PerSequencer: make(map[ledger.ChainID]utangle_old.SequencerSyncInfo),
//	}
//	for seqIDStr, si := range res.PerSequencer {
//		seqID, err := ledger.ChainIDFromHexString(seqIDStr)
//		if err != nil {
//			return utangle_old.SyncInfo{}, err
//		}
//		ret.PerSequencer[seqID] = utangle_old.SequencerSyncInfo{
//			Synced:           si.Synced,
//			LatestBookedSlot: si.LatestBookedSlot,
//			LatestSeenSlot:   si.LatestSeenSlot,
//		}
//	}
//	return ret, nil
//}

func (c *APIClient) QueryTxIDStatus(txid ledger.TransactionID) (mode, status string, err error) {
	path := fmt.Sprintf(api.PathQueryTxIDStatus+"?txid=%s", txid.StringHex())
	body, err := c.getBody(path)
	if err != nil {
		return "", "", err
	}

	var res api.QueryTxIDStatus
	err = json.Unmarshal(body, &res)
	if err != nil {
		return "", "", err
	}
	if res.Error.Error != "" {
		return "", "", fmt.Errorf("from server: %s", res.Error.Error)
	}
	return res.Mode, res.Status, res.Err
}

func (c *APIClient) GetNodeInfo() (*global.NodeInfo, error) {
	body, err := c.getBody(api.PathGetNodeInfo)
	if err != nil {
		return nil, err
	}
	return global.NodeInfoFromBytes(body)
}

func (c *APIClient) GetTransferableOutputs(account ledger.Accountable, ts ledger.Time, maxOutputs ...int) ([]*ledger.OutputWithID, uint64, error) {
	ret, err := c.GetAccountOutputs(account, func(o *ledger.Output) bool {
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

// MakeCompactTransaction requests server and creates a compact transaction for ED25519 outputs in the form of transaction context. Does not submit it
func (c *APIClient) MakeCompactTransaction(walletPrivateKey ed25519.PrivateKey, tagAlongSeqID *ledger.ChainID, tagAlongFee uint64, maxInputs ...int) (*transaction.TxContext, error) {
	walletAccount := ledger.AddressED25519FromPrivateKey(walletPrivateKey)

	nowisTs := ledger.TimeNow()
	inTotal := uint64(0)

	walletOutputs, inTotal, err := c.GetTransferableOutputs(walletAccount, nowisTs, maxInputs...)
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

	txCtx, err := transaction.TxContextFromTransferableBytes(txBytes, transaction.PickOutputFromListFunc(walletOutputs))
	if err != nil {
		return nil, err
	}
	return txCtx, err
}

type TransferFromED25519WalletParams struct {
	WalletPrivateKey ed25519.PrivateKey
	TagAlongSeqID    *ledger.ChainID
	TagAlongFee      uint64 // 0 means no fee output will be produced
	Amount           uint64
	Target           ledger.Lock
	MaxOutputs       int
}

const (
	minimumAmount = uint64(500)
)

func (c *APIClient) TransferFromED25519Wallet(par TransferFromED25519WalletParams) (*transaction.TxContext, error) {
	if par.Amount < minimumAmount {
		return nil, fmt.Errorf("minimum transfer amount is %d", minimumAmount)
	}
	walletAccount := ledger.AddressED25519FromPrivateKey(par.WalletPrivateKey)
	nowisTs := ledger.TimeNow()

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
	txCtx, err := transaction.TxContextFromTransferableBytes(txBytes, transaction.PickOutputFromListFunc(walletOutputs))
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

func (c *APIClient) MakeChainOrigin(par TransferFromED25519WalletParams) (*transaction.TxContext, ledger.ChainID, error) {
	if par.Amount < minimumAmount {
		return nil, ledger.NilChainID, fmt.Errorf("minimum transfer amount is %d", minimumAmount)
	}
	if par.Amount > 0 && par.TagAlongSeqID == nil {
		return nil, [32]byte{}, fmt.Errorf("tag-along sequencer not specified")
	}

	walletAccount := ledger.AddressED25519FromPrivateKey(par.WalletPrivateKey)

	ts := ledger.TimeNow()
	inps, totalInputs, err := c.GetTransferableOutputs(walletAccount, ts)
	if err != nil {
		return nil, [32]byte{}, err
	}
	if totalInputs < par.Amount+par.TagAlongFee {
		return nil, [32]byte{}, fmt.Errorf("not enough source balance %s", util.GoTh(totalInputs))
	}

	totalInputs = 0
	inps = util.PurgeSlice(inps, func(o *ledger.OutputWithID) bool {
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
	ts = ledger.MaxTime(ts1.AddTicks(ledger.TransactionPace()), ts)

	err = txb.PutStandardInputUnlocks(len(inps))
	util.AssertNoError(err)

	chainOut := ledger.NewOutput(func(o *ledger.Output) {
		_, _ = o.WithAmount(par.Amount).
			WithLock(par.Target).
			PushConstraint(ledger.NewChainOrigin().Bytes())
	})
	_, err = txb.ProduceOutput(chainOut)
	util.AssertNoError(err)

	if par.TagAlongFee > 0 {
		tagAlongFeeOut := ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(par.TagAlongFee).
				WithLock(ledger.ChainLockFromChainID(*par.TagAlongSeqID))
		})
		if _, err = txb.ProduceOutput(tagAlongFeeOut); err != nil {
			return nil, [32]byte{}, err
		}
	}

	if totalInputs > par.Amount+par.TagAlongFee {
		remainder := ledger.NewOutput(func(o *ledger.Output) {
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

	txCtx, err := transaction.TxContextFromTransferableBytes(txBytes, transaction.PickOutputFromListFunc(inps))
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
	Inputs        []*ledger.OutputWithID
	Target        ledger.Lock
	Amount        uint64
	Remainder     ledger.Lock
	PrivateKey    ed25519.PrivateKey
	TagAlongSeqID *ledger.ChainID
	TagAlongFee   uint64
	Timestamp     ledger.Time
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

	mainOut := ledger.NewOutput(func(o *ledger.Output) {
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
		feeOut := ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(par.TagAlongFee).
				WithLock(ledger.ChainLockFromChainID(*par.TagAlongSeqID))
		})
		if _, err = txb.ProduceOutput(feeOut); err != nil {
			return nil, err
		}
	}
	// produce remainder if needed
	if inTotal > par.Amount+par.TagAlongFee {
		remainderLock := par.Remainder
		if remainderLock == nil {
			remainderLock = ledger.AddressED25519FromPrivateKey(par.PrivateKey)
		}
		remainderOut := ledger.NewOutput(func(o *ledger.Output) {
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
