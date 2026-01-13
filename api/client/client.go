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
	"github.com/lunfardo314/proxima/core/core_modules/tippool"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
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

// GetLedgerIdentityData retrieves raw ledger identity YAML from server
func (c *APIClient) GetLedgerIdentityData() ([]byte, error) {
	body, err := c.getBody(api.PathGetLedgerIDData)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// getAccountOutputs fetches all outputs of the account. Optionally sorts them on the server
func (c *APIClient) getAccountOutputs(accountable ledger.Accountable, sort ...string) ([]*ledger.OutputDataWithID, *base.TransactionID, error) {
	path := fmt.Sprintf(api.PathGetAccountOutputs+"?accountable=%s", accountable.String())
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

	retLRBID, err := base.TransactionIDFromHexString(res.LRBID)
	if err != nil {
		return nil, nil, fmt.Errorf("while parsing transaction id: %s", res.Error.Error)
	}

	ret := make([]*ledger.OutputDataWithID, 0, len(res.Outputs))

	for idStr, dataStr := range res.Outputs {
		id, err := base.OutputIDFromHexString(idStr)
		if err != nil {
			return nil, nil, fmt.Errorf("wrong output id data from server: %s: '%v'", idStr, err)
		}
		oData, err := hex.DecodeString(dataStr)
		if err != nil {
			return nil, nil, fmt.Errorf("wrong output data from server: %s: '%v'", dataStr, err)
		}
		ret = append(ret, &ledger.OutputDataWithID{
			ID:   id,
			Data: oData,
		})
	}
	return ret, &retLRBID, nil
}

func (c *APIClient) GetSimpleSigLockedOutputs(addr ledger.AddressED25519, maxOutputs int, sort ...string) ([]*ledger.OutputWithID, *base.TransactionID, error) {
	path := fmt.Sprintf(api.PathGetAccountSimpleSiglockedOutputs+"?addr=%s", addr.Source())
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

	retLRBID, err := base.TransactionIDFromHexString(res.LRBID)
	if err != nil {
		return nil, nil, fmt.Errorf("while parsing transaction id: %s", res.Error.Error)
	}

	ret := make([]*ledger.OutputWithID, 0, len(res.Outputs))

	for idStr, dataStr := range res.Outputs {
		id, err := base.OutputIDFromHexString(idStr)
		if err != nil {
			return nil, nil, fmt.Errorf("wrong output id data from server: %s: '%w'", idStr, err)
		}
		o, err := ledger.OutputFromHexStringAtSlot(dataStr, base.MaxSlot)
		if err != nil {
			return nil, nil, fmt.Errorf("wrong output data from server: %s: '%w'", dataStr, err)
		}
		ret = append(ret, &ledger.OutputWithID{
			ID:     id,
			Output: o,
		})
	}
	return ret, &retLRBID, nil
}

// GetOutputsForAmount returns all UTXOs locked in the specified ED25519 address, which ar not chain outputs
func (c *APIClient) GetOutputsForAmount(addr ledger.AddressED25519, amount uint64) ([]*ledger.OutputWithID, *base.TransactionID, uint64, error) {
	path := fmt.Sprintf(api.PathGetOutputsForAmount+"?addr=%s&amount=%d", addr.Source(), amount)
	body, err := c.getBody(path)
	if err != nil {
		return nil, nil, 0, err
	}

	var res api.OutputList
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, nil, 0, err
	}
	if res.Error.Error != "" {
		return nil, nil, 0, fmt.Errorf("from server: %s", res.Error.Error)
	}

	retLRBID, err := base.TransactionIDFromHexString(res.LRBID)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("while parsing transaction id: %s", res.Error.Error)
	}

	ret := make([]*ledger.OutputWithID, 0, len(res.Outputs))

	sum := uint64(0)
	for idStr, dataStr := range res.Outputs {
		id, err := base.OutputIDFromHexString(idStr)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("wrong output id data from server: %s: '%w'", idStr, err)
		}
		o, err := ledger.OutputFromHexStringAtSlot(dataStr, base.MaxSlot)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("wrong output data from server: %s: '%w'", dataStr, err)
		}
		ret = append(ret, &ledger.OutputWithID{
			ID:     id,
			Output: o,
		})
		sum += o.TokenBalance()
	}
	if sum < amount {
		// double check
		return nil, nil, 0, fmt.Errorf("inconsistency: server returned not enough tokens")
	}
	return ret, &retLRBID, sum, nil
}

// GetNonChainBalance total of outputs locked in the account but without chain constraint
func (c *APIClient) GetNonChainBalance(addr ledger.Accountable) (uint64, *base.TransactionID, error) {
	path := fmt.Sprintf(api.PathGetNonChainBalance+"?addr=%s", addr.Source())
	body, err := c.getBody(path)
	if err != nil {
		return 0, nil, err
	}

	var res api.Balance
	err = json.Unmarshal(body, &res)
	if err != nil {
		return 0, nil, err
	}
	if res.Error.Error != "" {
		return 0, nil, fmt.Errorf("from server: %s", res.Error.Error)
	}
	retLRBID, err := base.TransactionIDFromHexString(res.LRBID)
	if err != nil {
		return 0, nil, fmt.Errorf("while parsing transaction id: %s", res.Error.Error)
	}
	return res.Amount, &retLRBID, nil
}

// GetChainedOutputs fetches all outputs of the account. Optionally sorts them on the server
func (c *APIClient) GetChainedOutputs(accountable ledger.Accountable) ([]*ledger.OutputWithChainID, *base.TransactionID, error) {
	path := fmt.Sprintf(api.PathGetChainedOutputs+"?accountable=%s", accountable.String())
	return c._getChainedOutputs(path)
}

// GetDelegationOutputs fetches all delegation outputs of the account. Optionally sorts them on the server
func (c *APIClient) GetDelegationOutputs(accountable ledger.Accountable) ([]ledger.DelegationOutput, *base.TransactionID, error) {
	path := fmt.Sprintf(api.PathGetDelegationOutputs+"?accountable=%s", accountable.String())
	outs, lrbid, err := c._getChainedOutputs(path)
	if err != nil {
		return nil, nil, err
	}
	ret := make([]ledger.DelegationOutput, 0, len(outs))
	for _, out := range outs {
		if o, ok := ledger.AsDelegationOutput(out.Output, out.ID); ok {
			ret = append(ret, o)
		}
	}
	return ret, lrbid, nil
}

func (c *APIClient) _getChainedOutputs(path string) ([]*ledger.OutputWithChainID, *base.TransactionID, error) {
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

	retLRBID, err := base.TransactionIDFromHexString(res.LRBID)
	if err != nil {
		return nil, nil, fmt.Errorf("while parsing transaction id: %s", res.Error.Error)
	}

	ret := make([]*ledger.OutputWithChainID, 0, len(res.Outputs))

	for idStr, dataStr := range res.Outputs {
		oid, err := base.OutputIDFromHexString(idStr)
		if err != nil {
			return nil, nil, fmt.Errorf("wrong output id data from server: %s: '%v'", idStr, err)
		}
		oData, err := hex.DecodeString(dataStr)
		if err != nil {
			return nil, nil, fmt.Errorf("wrong output data from server: %s: '%v'", dataStr, err)
		}
		// API client uses latest library version for parsing outputs received from server
		o, err := ledger.OutputFromBytesAtSlot(oData, base.MaxSlot)
		if err != nil {
			return nil, nil, fmt.Errorf("wrong output data from server: %s: '%v'", dataStr, err)
		}

		ret1, ok := ledger.AsOutputWithChainID(o, oid)
		if !ok {
			return nil, nil, fmt.Errorf("not a chain output: ChainID=%s:\n%s:", oid.String(), o.String())
		}
		ret = append(ret, &ret1)
	}
	return ret, &retLRBID, nil
}

func (c *APIClient) GetChainOutputData(chainID base.ChainID) (*ledger.OutputDataWithID, base.TransactionID, error) {
	path := fmt.Sprintf(api.PathGetChainOutput+"?chainid=%s", chainID.StringHex())
	body, err := c.getBody(path)
	if err != nil {
		return nil, base.TransactionID{}, err
	}

	var res api.ChainOutput
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, base.TransactionID{}, err
	}
	if res.Error.Error != "" {
		if strings.Contains(res.Error.Error, "object not found") {
			return nil, base.TransactionID{}, multistate.ErrNotFound
		}
		return nil, base.TransactionID{}, fmt.Errorf("GetChainOutputData for %s: from server: %s", chainID.StringShort(), res.Error.Error)
	}

	oid, err := base.OutputIDFromHexString(res.ID)
	if err != nil {
		return nil, base.TransactionID{}, fmt.Errorf("GetChainOutputData for %s: wrong output id data received from server: %s: '%v",
			chainID.StringShort(), res.ID, err)
	}
	oData, err := hex.DecodeString(res.Data)
	if err != nil {
		return nil, base.TransactionID{}, fmt.Errorf("wrong output data received from server: %s: '%v'", res.Data, err)
	}

	lrb, err := base.TransactionIDFromHexString(res.LRBID)
	if err != nil {
		return nil, base.TransactionID{}, fmt.Errorf("wrong LRBID data received from server: %s: '%v'", res.LRBID, err)
	}

	return &ledger.OutputDataWithID{
		ID:   oid,
		Data: oData,
	}, lrb, nil
}

// GetChainOutput returns parsed output for the chain id and index of the chain constraint in it
func (c *APIClient) GetChainOutput(chainID base.ChainID) (*ledger.OutputWithChainID, byte, base.TransactionID, error) {
	oData, lrbid, err := c.GetChainOutputData(chainID)
	if err != nil {
		return nil, 0, base.TransactionID{}, err
	}
	o, constrIdx, err := oData.ParseAsChainOutput()
	if err != nil {
		return nil, 0, base.TransactionID{}, err
	}
	return o, constrIdx, lrbid, nil
}

func (c *APIClient) GetSequencerData(chainID base.ChainID) (ret seqdata.SequencerData, err error) {
	o, _, _, err := c.GetChainOutput(chainID)
	if err != nil {
		err = fmt.Errorf("GetSequencerData: error while retrieving UTXO for %s: %w", chainID.StringShort(), err)
		return
	}
	if !o.ID.IsSequencerTransaction() {
		err = fmt.Errorf("GetSequencerData: not a sequencer output: %s", chainID.StringShort())
	}
	return ledger.ParseSequencerData(o.Output)
}

// GetOutputData returns output data from the LRB state, if it exists there
// Returns nil, nil if output does not exist
func (c *APIClient) GetOutputData(oid *base.OutputID) ([]byte, error) {
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
	defer func() { _ = resp.Body.Close() }()

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

// GetAccountOutputs returns all UTXOs in the account
func (c *APIClient) GetAccountOutputs(account ledger.Accountable, filter ...func(oid *base.OutputID, o *ledger.Output) bool) ([]*ledger.OutputWithID, *base.TransactionID, error) {
	return c.GetAccountOutputsExt(account, "", filter...)
}

func (c *APIClient) GetAccountOutputsExt(account ledger.Accountable, sortOption string, filter ...func(oid *base.OutputID, o *ledger.Output) bool) ([]*ledger.OutputWithID, *base.TransactionID, error) {
	filterFun := func(oid *base.OutputID, o *ledger.Output) bool { return true }
	if len(filter) > 0 {
		filterFun = filter[0]
	}
	oData, lrbid, err := c.getAccountOutputs(account, sortOption)
	if err != nil {
		return nil, nil, err
	}

	outs, err := ledger.ParseOutputDataAndFilter(oData, filterFun)
	if err != nil {
		return nil, nil, err
	}
	return outs, lrbid, nil
}

func (c *APIClient) GetAccountParsedOutputs(account ledger.Accountable, maxOutputs int, sortOption ...string) (*api.ParsedOutputList, error) {
	if maxOutputs < 0 {
		maxOutputs = 0
	}
	path := fmt.Sprintf(api.PathGetAccountParsedOutputs+"?accountable=%s", account.String())
	if maxOutputs > 0 {
		path += fmt.Sprintf("&max_outputs=%d", maxOutputs)
	}
	if len(sortOption) > 0 {
		switch {
		case strings.HasPrefix(sortOption[0], "desc"):
			path += "&sort=desc"
		case strings.HasPrefix(sortOption[0], "asc"):
			path += "&sort=asc"
		}
	}
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}

	var res api.ParsedOutputList
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, err
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}
	return &res, nil
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

func (c *APIClient) GetAllChains() ([]*ledger.OutputWithChainID, *base.TransactionID, error) {
	body, err := c.getBody(api.PathGetAllChains)
	if err != nil {
		return nil, nil, err
	}

	var res api.Chains
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, nil, err
	}
	if res.Error.Error != "" {
		return nil, nil, fmt.Errorf("%s", res.Error.Error)
	}

	lrbid, err := base.TransactionIDFromHexString(res.LRBID)
	if err != nil {
		return nil, nil, err
	}

	ret := make([]*ledger.OutputWithChainID, 0, len(res.Chains))
	for _, ci := range res.Chains {
		o, err := ledger.OutputFromHexStringAtSlot(ci.Data, base.MaxSlot)
		if err != nil {
			return nil, nil, err
		}
		oid, err := base.OutputIDFromHexString(ci.ID)
		if err != nil {
			return nil, nil, err
		}

		cData, ok := ledger.AsOutputWithChainID(o, oid)
		if !ok {
			return nil, nil, fmt.Errorf("invalid chain constraint")
		}
		ret = append(ret, &cData)
	}
	return ret, &lrbid, nil
}

// GetTransferableOutputs returns a reasonable maximum number of outputs owned by accountable with only 2 constraints and returns total
func (c *APIClient) GetTransferableOutputs(account ledger.Accountable, maxOutputs ...int) ([]*ledger.OutputWithID, *base.TransactionID, uint64, error) {
	maxO := 256
	if len(maxOutputs) > 0 && maxOutputs[0] < 256 && maxOutputs[0] > 0 {
		maxO = maxOutputs[0]
	}

	// ask a bit more descending outputs from server and the filter them out
	ret, lrbid, err := c.GetAccountOutputsExt(account, "desc", func(_ *base.OutputID, o *ledger.Output) bool {
		return o.NumConstraints() == 2
	})
	if err != nil {
		return nil, nil, 0, err
	}
	if len(ret) == 0 {
		return nil, nil, 0, nil
	}
	ret = util.PurgeSlice(ret, func(o *ledger.OutputWithID) bool {
		return ledger.EqualAccountables(account, o.Output.Lock().Master())
	})
	ret = util.TrimSlice(ret, maxO)
	sum := uint64(0)
	for _, o := range ret {
		sum += o.Output.TokenBalance()
	}
	return ret, lrbid, sum, nil
}

// MakeCompactTransaction requests server and creates a compact transaction for ED25519 outputs in the form of transaction context. Does not submit it
func (c *APIClient) MakeCompactTransaction(walletPrivateKey ed25519.PrivateKey, tagAlongSeqID *base.ChainID, tagAlongFee uint64, maxInputs ...int) (*transaction.TxContext, error) {
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
	TagAlongSeqID    *base.ChainID
	TagAlongFee      uint64 // 0 means no fee output will be produced
	Amount           uint64
	Target           ledger.Lock
	MaxOutputs       int
}

const minimumTransferAmount = uint64(1000)

func (c *APIClient) TransferFromED25519Wallet(par TransferFromED25519WalletParams) (*transaction.TxContext, error) {
	if par.Amount < minimumTransferAmount {
		return nil, fmt.Errorf("minimum transfer amount is %d", minimumTransferAmount)
	}
	walletAccount := ledger.AddressED25519FromPrivateKey(par.WalletPrivateKey)
	walletOutputs, _, _, err := c.GetOutputsForAmount(walletAccount, par.Amount+par.TagAlongFee)
	if err != nil {
		return nil, err
	}
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
	txCtx, err := transaction.TxContextFromTransferableBytes(txBytes, transaction.PickOutputFromListFunc(walletOutputs))
	if err != nil {
		return nil, err
	}
	err = c.SubmitTransaction(txBytes)
	return txCtx, err
}

func (c *APIClient) Get(path string) ([]byte, error) {
	return c.getBody(path)
}

func (c *APIClient) getBody(path string) ([]byte, error) {
	url := c.prefix + path
	resp, err := c.c.Get(url)
	if err != nil {
		return nil, fmt.Errorf("GET returned: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("io.ReadAll returned: %v", err)
	}
	return body, nil
}

func (c *APIClient) MakeChainOrigin(par TransferFromED25519WalletParams) (*transaction.TxContext, base.ChainID, error) {
	if par.Amount < minimumTransferAmount {
		return nil, base.NilChainID, fmt.Errorf("minimum transfer amount is %d", minimumTransferAmount)
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
	ts = base.MaximumTime(ts1.AddTicks(int(ledger.Const.TransactionPace)), ts)

	err = txb.PutStandardInputUnlocks(len(inps))
	util.AssertNoError(err)

	chainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(par.Amount).
			WithLock(par.Target).
			MustPushConstraint(ledger.NewChainOrigin(ts.Slot, par.Amount).Bytes())
	})
	_, err = txb.ProduceOutput(chainOut)
	util.AssertNoError(err)

	if par.TagAlongFee > 0 {
		tagAlongFeeOut := ledger.NewTagAlongOutput(par.TagAlongFee, *par.TagAlongSeqID, ledger.AddressED25519FromPrivateKey(par.WalletPrivateKey))
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
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
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

// GetLatestReliableBranch retrieves latest reliable branch info from the node
func (c *APIClient) GetLatestReliableBranch() (*multistate.RootRecord, base.TransactionID, error) {
	body, err := c.getBody(api.PathGetLatestReliableBranch)
	if err != nil {
		return nil, base.TransactionID{}, err
	}

	var res api.LatestReliableBranch
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, base.TransactionID{}, fmt.Errorf("unmarshal returned: %v\nbody: '%s'", err, string(body))
	}
	if res.Error.Error != "" {
		return nil, base.TransactionID{}, fmt.Errorf("from server: %s", res.Error.Error)
	}

	rr, err := res.RootData.Parse()
	if err != nil {
		return nil, base.TransactionID{}, fmt.Errorf("parse failed: %v", err)
	}
	return rr, res.BranchID, nil
}

func (c *APIClient) GetSnapshotBranchID() (ret base.TransactionID, err error) {
	body, err := c.getBody(api.PathGetSnapshotBranchID)
	if err != nil {
		return
	}
	var res api.SnapshotID
	err = json.Unmarshal(body, &res)
	if err != nil {
		err = fmt.Errorf("unmarshal returned: %v\nbody: '%s'", err, string(body))
		return
	}
	if res.Error.Error != "" {
		err = fmt.Errorf("from server: %s", res.Error.Error)
		return
	}
	return base.TransactionIDFromHexString(res.ID)
}

func (c *APIClient) GetLastKnownSequencerData() (map[string]tippool.LatestSequencerTipDataJSONAble, error) {
	body, err := c.getBody(api.PathGetLastKnownSequencerMilestones)
	if err != nil {
		return nil, err
	}

	var res api.KnownLatestMilestones
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, fmt.Errorf("unmarshal returned: %v\nbody: '%s'", err, string(body))
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}
	return res.Sequencers, nil
}

func (c *APIClient) CheckTransactionIDInLRB(txid base.TransactionID, maxDepth ...int) (lrbID base.TransactionID, foundAtDepth int, err error) {
	path := api.PathCheckTxIDInLRB + "?txid=" + txid.StringHex()
	if len(maxDepth) > 0 {
		path += fmt.Sprintf("&max_depth=%d", maxDepth[0])
	}
	body, err := c.getBody(path)
	if err != nil {
		return
	}

	var res api.CheckTxIDInLRB
	err = json.Unmarshal(body, &res)
	if err != nil {
		err = fmt.Errorf("unmarshal returned: %v\nbody: '%s'", err, string(body))
		return
	}
	if res.Error.Error != "" {
		err = fmt.Errorf("from server: %s", res.Error.Error)
		return
	}
	var resTxID base.TransactionID
	resTxID, err = base.TransactionIDFromHexString(res.TxID)
	if err != nil {
		return
	}
	if resTxID != txid {
		return base.TransactionID{}, -1, fmt.Errorf("inconsistency: wrong txid from server")
	}
	lrbID, err = base.TransactionIDFromHexString(res.LRBID)
	if err != nil {
		return
	}
	foundAtDepth = res.FoundAtDepth
	return
}

func (c *APIClient) GetInactiveUTXOs(slotsBack ...int) (ret api.InactiveUTXOs, err error) {
	path := api.PathGetInactive
	if len(slotsBack) > 0 {
		path += fmt.Sprintf("?slots_back=%d", slotsBack[0])
	}
	body, err := c.getBody(path)
	if err != nil {
		return
	}
	err = json.Unmarshal(body, &ret)
	if err != nil {
		err = fmt.Errorf("unmarshal returned: %v\nbody: '%s'", err, string(body))
		return
	}
	if ret.Error.Error != "" {
		err = fmt.Errorf("from server: %s", ret.Error.Error)
		return
	}
	return
}

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
		tagAlongOut := ledger.NewTagAlongOutput(par.TagAlongFee, *par.TagAlongSeqID, ledger.AddressED25519FromPrivateKey(par.PrivateKey))
		if _, err = txb.ProduceOutput(tagAlongOut); err != nil {
			return nil, err
		}
	}
	// produce remainder if needed
	if inTotal > par.Amount+par.TagAlongFee {
		remainderLock := par.Remainder
		if remainderLock == nil {
			remainderLock = ledger.AddressED25519FromPrivateKey(par.PrivateKey)
		}
		remainderOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(inTotal - par.Amount - par.TagAlongFee).
				WithLock(remainderLock)
		})
		if _, err = txb.ProduceOutput(remainderOut); err != nil {
			return nil, err
		}
	}

	txb.TransactionData.Timestamp = par.Timestamp
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(par.PrivateKey)

	txBytes, _, txString, err := txb.BytesWithValidation()

	if err != nil {
		err = fmt.Errorf("%v\n------ failing transaction -------\n%s", err, txString)
	}

	return txBytes, err
}

func (c *APIClient) MakeSendOutputTransaction(o *ledger.Output, privateKey ed25519.PrivateKey, ts base.LedgerTime) ([]byte, base.TransactionID, string, error) {
	account := ledger.AddressED25519FromPrivateKey(privateKey)
	walletOutputs, _, amountInWallet, err := c.GetTransferableOutputs(account, 255)
	if err != nil {
		return nil, base.TransactionID{}, "", err
	}
	bal := o.TokenBalance()
	if amountInWallet < bal {
		return nil, base.TransactionID{}, "", fmt.Errorf("not enough balance")
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

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(privateKey)

	txBytes, txid, txString, err := txb.BytesWithValidation()
	if err != nil {
		return nil, base.TransactionID{}, txString, err
	}
	return txBytes, txid, txString, nil
}

type SequencerData struct {
	ledger.OutputWithChainID
	NumDelegations int
}

func (c *APIClient) GetAllSequencerOutputs() (map[base.ChainID]ledger.OutputWithSequencerData, *base.TransactionID, error) {
	body, err := c.getBody(api.PathGetSequencers)
	if err != nil {
		return nil, nil, err
	}

	var res api.Sequencers
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, nil, err
	}
	if res.Error.Error != "" {
		return nil, nil, fmt.Errorf("%s", res.Error.Error)
	}
	lrbid, err := base.TransactionIDFromHexString(res.LRBID)
	if err != nil {
		return nil, nil, err
	}
	ret := make(map[base.ChainID]ledger.OutputWithSequencerData)
	for chainIDStr, data := range res.OutputData {
		if data.Data == "" || data.ID == "" {
			// skip sequencer ID that do not have outputs. This may happen when delegation target sequencer does not exit
			continue
		}
		seqID, err := base.ChainIDFromHexString(chainIDStr)
		if err != nil {
			return nil, nil, err
		}
		seqOutID, err := base.OutputIDFromHexString(data.ID)
		if err != nil {
			return nil, nil, err
		}
		o, err := ledger.OutputFromHexStringAtSlot(data.Data, base.MaxSlot)
		if err != nil {
			return nil, nil, err
		}
		seqOutData, isSeqOut := o.SequencerOutputData()
		if !isSeqOut {
			return nil, nil, fmt.Errorf("not a sequencer output: %s", data.ID)
		}
		if seqID != seqOutData.ChainConstraint.ChainID {
			return nil, nil, fmt.Errorf("inconsistency: chain IDs does not match")
		}
		ret[seqID] = ledger.OutputWithSequencerData{
			OutputWithID: ledger.OutputWithID{
				Output: o,
				ID:     seqOutID,
			},
			SequencerOutputData: *seqOutData,
		}
	}
	return ret, &lrbid, nil
}
