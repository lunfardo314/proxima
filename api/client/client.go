package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
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

// not useful, too big delays with DNS names
//func New(serverURL string, timeout ...time.Duration) *APIClient {
//	var to time.Duration
//	if len(timeout) > 0 {
//		to = timeout[0]
//	} else {
//		to = apiDefaultClientTimeout
//	}
//	return &APIClient{
//		c:      http.Client{Timeout: to},
//		prefix: serverURL,
//	}
//}

// NewWithGoogleDNS following ChatGPT suggestion to use GoogleDNS to speed up DNS name resolution
// Otherwise it takes too long in Proxi
func NewWithGoogleDNS(serverURL string, timeout ...time.Duration) *APIClient {
	const (
		dnsResolverTimeout = time.Millisecond * 500
		googleDNSAddr      = "8.8.8.8:53"
	)

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: dnsResolverTimeout,
			}
			return d.DialContext(ctx, network, googleDNSAddr)
		},
	}
	// Create a custom HTTP transport with the custom resolver
	dialer := &net.Dialer{
		Resolver: resolver,
	}
	transport := &http.Transport{
		DialContext: dialer.DialContext,
	}

	var to time.Duration
	if len(timeout) > 0 {
		to = timeout[0]
	} else {
		to = apiDefaultClientTimeout
	}
	return &APIClient{
		c: http.Client{
			Transport: transport,
			Timeout:   to,
		},
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
		ret = ledger.MustIdentityDataFromBytes(idBin)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("GetLedgerID: error while parsing received data: %w", err)
	}
	return ret, nil
}

// getAccountOutputs fetches all outputs of the account. Optionally sorts them on the server
func (c *APIClient) getAccountOutputs(accountable ledger.Accountable, maxOutputs int, sort ...string) ([]*ledger.OutputDataWithID, *ledger.TransactionID, error) {
	if maxOutputs < 0 {
		maxOutputs = 0
	}
	path := fmt.Sprintf(api.PathGetAccountOutputs+"?accountable=%s", accountable.String())
	if maxOutputs > 0 {
		path += fmt.Sprintf("&max_outputs=%d", maxOutputs)
	}
	if len(sort) > 0 {
		switch {
		case strings.HasPrefix(sort[0], "desc"):
			path += "&sort=desc"
		case strings.HasPrefix(sort[0], "asc"):
			path += "&sort=asc"
		}
	}
	body, err := c.getBody(path)
	if err != nil {
		return nil, nil, err
	}

	var res api.OutputList
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, nil, err
	}
	if res.Error.Error != "" {
		return nil, nil, fmt.Errorf("from server: %s", res.Error.Error)
	}

	retLRBID, err := ledger.TransactionIDFromHexString(res.LRBID)
	if err != nil {
		return nil, nil, fmt.Errorf("while parsing transaction ID: %s", res.Error.Error)
	}

	ret := make([]*ledger.OutputDataWithID, 0, len(res.Outputs))

	for idStr, dataStr := range res.Outputs {
		id, err := ledger.OutputIDFromHexString(idStr)
		if err != nil {
			return nil, nil, fmt.Errorf("wrong output ID data from server: %s: '%v'", idStr, err)
		}
		oData, err := hex.DecodeString(dataStr)
		if err != nil {
			return nil, nil, fmt.Errorf("wrong output data from server: %s: '%v'", dataStr, err)
		}
		ret = append(ret, &ledger.OutputDataWithID{
			ID:         id,
			OutputData: oData,
		})
	}
	return ret, &retLRBID, nil
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
		return nil, fmt.Errorf("GetChainOutputData for %s: from server: %s", chainID.StringShort(), res.Error.Error)
	}

	oid, err := ledger.OutputIDFromHexString(res.OutputID)
	if err != nil {
		return nil, fmt.Errorf("GetChainOutputData for %s: wrong output ID data received from server: %s: '%v\n-----\n%s'",
			chainID.StringShort(), res.OutputID, err, string(body))
	}
	oData, err := hex.DecodeString(res.OutputData)
	if err != nil {
		return nil, fmt.Errorf("wrong output data received from server: %s: '%v'", res.OutputData, err)
	}

	return &ledger.OutputDataWithID{
		ID:         oid,
		OutputData: oData,
	}, nil
}

// GetChainOutput returns parsed output for the chain ID and index of the chain constraint in it
func (c *APIClient) GetChainOutput(chainID ledger.ChainID) (*ledger.OutputWithChainID, byte, error) {
	oData, err := c.GetChainOutputData(chainID)
	if err != nil {
		return nil, 0, err
	}
	return oData.ParseAsChainOutput()
}

func (c *APIClient) GetMilestoneData(chainID ledger.ChainID) (*ledger.MilestoneData, error) {
	o, _, err := c.GetChainOutput(chainID)
	if err != nil {
		return nil, fmt.Errorf("error while retrieving milestone for sequencer %s: %w", chainID.StringShort(), err)
	}
	if !o.ID.IsSequencerTransaction() {
		return nil, fmt.Errorf("not a sequencer milestone: %s", chainID.StringShort())
	}
	return ledger.ParseMilestoneData(o.Output), nil
}

// GetOutputData returns output data from the LRB state, if it exists there
// Returns nil, nil if output does not exist
func (c *APIClient) GetOutputData(oid *ledger.OutputID) ([]byte, error) {
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

func (c *APIClient) SubmitTransaction(txBytes []byte, trace ...bool) error {
	url := c.prefix + api.PathSubmitTransaction
	if len(trace) > 0 && trace[0] {
		url += "?trace=true"
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

func (c *APIClient) GetAccountOutputs(account ledger.Accountable, filter ...func(oid *ledger.OutputID, o *ledger.Output) bool) ([]*ledger.OutputWithID, *ledger.TransactionID, error) {
	return c.GetAccountOutputsExt(account, 0, "", filter...)
}

func (c *APIClient) GetAccountOutputsExt(account ledger.Accountable, maxOutputs int, sortOption string, filter ...func(oid *ledger.OutputID, o *ledger.Output) bool) ([]*ledger.OutputWithID, *ledger.TransactionID, error) {
	filterFun := func(oid *ledger.OutputID, o *ledger.Output) bool { return true }
	if len(filter) > 0 {
		filterFun = filter[0]
	}
	oData, lrbid, err := c.getAccountOutputs(account, maxOutputs, sortOption)
	if err != nil {
		return nil, nil, err
	}

	outs, err := txutils.ParseOutputDataAndFilter(oData, filterFun)
	if err != nil {
		return nil, nil, err
	}
	return outs, lrbid, nil
}

func (c *APIClient) QueryTxIDStatus(txid ledger.TransactionID, slotSpan int) (*vertex.TxIDStatus, *multistate.TxInclusion, error) {
	path := fmt.Sprintf(api.PathQueryTxStatus+"?txid=%s&slots=%d", txid.StringHex(), slotSpan)
	body, err := c.getBody(path)
	if err != nil {
		return nil, nil, err
	}

	var res api.QueryTxStatus

	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, nil, fmt.Errorf("unmarshal: %w", err)
	}
	if res.Error.Error != "" {
		return nil, nil, fmt.Errorf("from server: %s", res.Error.Error)
	}

	retTxIDStatus, err := res.TxIDStatus.Parse()
	if err != nil {
		return nil, nil, err
	}

	retInclusion, err := res.Inclusion.Parse()
	if err != nil {
		return nil, nil, err
	}

	return retTxIDStatus, retInclusion, nil
}

func (c *APIClient) QueryTxInclusionScore(txid ledger.TransactionID, thresholdNumerator, thresholdDenominator, slotSpan int) (*api.TxInclusionScore, error) {
	path := fmt.Sprintf(api.PathQueryInclusionScore+"?txid=%s&threshold=%d-%d&slots=%d",
		txid.StringHex(), thresholdNumerator, thresholdDenominator, slotSpan)
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}
	var res api.QueryTxInclusionScore
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, fmt.Errorf("unmarshal returned: %v\nbody: '%s'", err, string(body))
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}
	return &res.TxInclusionScore, nil
}

func (c *APIClient) GetNodeInfo() (*global.NodeInfo, error) {
	body, err := c.getBody(api.PathGetNodeInfo)
	if err != nil {
		return nil, err
	}
	return global.NodeInfoFromBytes(body)
}

func (c *APIClient) GetSyncInfo() (*api.SyncInfo, error) {
	body, err := c.getBody(api.PathGetSyncInfo)
	if err != nil {
		return nil, err
	}

	var res api.SyncInfo
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, fmt.Errorf("unmarshal returned: %v\nbody: '%s'", err, string(body))
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}
	return &res, nil
}

func (c *APIClient) GetPeersInfo() (*api.PeersInfo, error) {
	body, err := c.getBody(api.PathGetPeersInfo)
	if err != nil {
		return nil, err
	}

	var res api.PeersInfo
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, fmt.Errorf("unmarshal returned: %v\nbody: '%s'", err, string(body))
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}
	return &res, nil
}

// GetTransferableOutputs returns reasonable maximum number of outputs with only 2 constraints and returns total
func (c *APIClient) GetTransferableOutputs(account ledger.Accountable, maxOutputs ...int) ([]*ledger.OutputWithID, *ledger.TransactionID, uint64, error) {
	maxO := 256
	if len(maxOutputs) > 0 && maxOutputs[0] < 256 && maxOutputs[0] > 0 {
		maxO = maxOutputs[0]
	}

	// ask a bit more descending outputs from server and the filter them out
	ret, lrbid, err := c.GetAccountOutputsExt(account, maxO*2, "desc", func(_ *ledger.OutputID, o *ledger.Output) bool {
		return o.NumConstraints() == 2
	})
	if err != nil {
		return nil, nil, 0, err
	}
	if len(ret) == 0 {
		return nil, nil, 0, nil
	}
	ret = util.TrimSlice(ret, maxO)
	sum := uint64(0)
	for _, o := range ret {
		sum += o.Output.Amount()
	}
	return ret, lrbid, sum, nil
}

// MakeCompactTransaction requests server and creates a compact transaction for ED25519 outputs in the form of transaction context. Does not submit it
func (c *APIClient) MakeCompactTransaction(walletPrivateKey ed25519.PrivateKey, tagAlongSeqID *ledger.ChainID, tagAlongFee uint64, maxInputs ...int) (*transaction.TxContext, error) {
	walletAccount := ledger.AddressED25519FromPrivateKey(walletPrivateKey)

	nowisTs := ledger.TimeNow()
	inTotal := uint64(0)

	walletOutputs, _, inTotal, err := c.GetTransferableOutputs(walletAccount, maxInputs...)
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
	TraceTx          bool
}

const minimumTransferAmount = uint64(1000)

func (c *APIClient) TransferFromED25519Wallet(par TransferFromED25519WalletParams) (*transaction.TxContext, error) {
	if par.Amount < minimumTransferAmount {
		return nil, fmt.Errorf("minimum transfer amount is %d", minimumTransferAmount)
	}
	walletAccount := ledger.AddressED25519FromPrivateKey(par.WalletPrivateKey)
	nowisTs := ledger.TimeNow()

	walletOutputs, _, _, err := c.GetTransferableOutputs(walletAccount, par.MaxOutputs)

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
	err = c.SubmitTransaction(txBytes, par.TraceTx)
	return txCtx, err
}

func (c *APIClient) getBody(path string) ([]byte, error) {
	url := c.prefix + path
	resp, err := c.c.Get(url)
	if err != nil {
		return nil, fmt.Errorf("GET returned: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("io.ReadAll returned: %v", err)
	}
	return body, nil
}

func (c *APIClient) MakeChainOrigin(par TransferFromED25519WalletParams) (*transaction.TxContext, ledger.ChainID, error) {
	if par.Amount < minimumTransferAmount {
		return nil, ledger.NilChainID, fmt.Errorf("minimum transfer amount is %d", minimumTransferAmount)
	}
	if par.Amount > 0 && par.TagAlongSeqID == nil {
		return nil, [32]byte{}, fmt.Errorf("tag-along sequencer not specified")
	}

	walletAccount := ledger.AddressED25519FromPrivateKey(par.WalletPrivateKey)

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
	ts = ledger.MaximumTime(ts1.AddTicks(ledger.TransactionPace()), ts)

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

type DeleteChainOriginParams struct {
	WalletPrivateKey ed25519.PrivateKey
	TagAlongSeqID    *ledger.ChainID
	TagAlongFee      uint64 // 0 means no fee output will be produced
	ChainID          *ledger.ChainID
	TraceTx          bool
}

func (c *APIClient) DeleteChainOrigin(par DeleteChainOriginParams) (*transaction.TxContext, error) {
	if par.TagAlongFee > 0 && par.TagAlongSeqID == nil {
		return nil, fmt.Errorf("tag-along sequencer not specified")
	}

	chainId := *par.ChainID
	chainIN, _, err := c.GetChainOutput(chainId)
	util.AssertNoError(err)

	ts := ledger.TimeNow()

	_, predecessorConstraintIndex := chainIN.Output.ChainConstraint()

	txb := txbuilder.NewTransactionBuilder()

	ts1 := chainIN.Timestamp()
	consumedIndex, err := txb.ConsumeOutput(chainIN.Output, chainIN.ID)
	util.AssertNoError(err)
	ts = ledger.MaximumTime(ts1.AddTicks(ledger.TransactionPace()), ts)

	feeAmount := par.TagAlongFee

	outNonChain := ledger.NewOutput(func(o *ledger.Output) {
		o.WithAmount(chainIN.Output.Amount() - feeAmount).
			WithLock(chainIN.Output.Lock())
	})
	_, err = txb.ProduceOutput(outNonChain)
	util.AssertNoError(err)

	if feeAmount > 0 {
		tagAlongFeeOut := ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(feeAmount).
				WithLock(ledger.ChainLockFromChainID(*par.TagAlongSeqID))
		})
		if _, err = txb.ProduceOutput(tagAlongFeeOut); err != nil {
			return nil, err
		}
	}

	txb.PutUnlockParams(consumedIndex, predecessorConstraintIndex, []byte{0xff, 0xff, 0xff})

	txb.PutSignatureUnlock(consumedIndex)

	// finalize the transaction
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(par.WalletPrivateKey)

	txBytes := txb.TransactionData.Bytes()

	inps := make([]*ledger.OutputWithID, 1)
	inps[0] = &chainIN.OutputWithID
	txCtx, err := transaction.TxContextFromTransferableBytes(txBytes, transaction.PickOutputFromListFunc(inps))
	if err != nil {
		return nil, err
	}
	if err = c.SubmitTransaction(txBytes, par.TraceTx); err != nil {
		return nil, err
	}

	return txCtx, err
}

// GetLatestReliableBranch retrieves lates reliable branch info from the node
func (c *APIClient) GetLatestReliableBranch() (*multistate.RootRecord, *ledger.TransactionID, error) {
	body, err := c.getBody(api.PathGetLatestReliableBranch)
	if err != nil {
		return nil, nil, err
	}

	var res api.LatestReliableBranch
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, nil, fmt.Errorf("unmarshal returned: %v\nbody: '%s'", err, string(body))
	}
	if res.Error.Error != "" {
		return nil, nil, fmt.Errorf("from server: %s", res.Error.Error)
	}

	rr, err := res.RootData.Parse()
	if err != nil {
		return nil, nil, fmt.Errorf("parse failed: %v", err)
	}
	return rr, &res.BranchID, nil
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
	if par.Amount < minimumTransferAmount {
		return nil, fmt.Errorf("minimum transfer amount is %d", minimumTransferAmount)
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
