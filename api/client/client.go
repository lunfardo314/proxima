package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
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

// GetLedgerDefinition retrieves ledger definition for a specific slot from server.
// If slot is nil, returns the latest definition (MaxSlot).
func (c *APIClient) GetLedgerDefinition(slot *uint32) (*api.LedgerDefinition, error) {
	path := api.PathGetLedgerDefinition
	if slot != nil {
		path = fmt.Sprintf("%s?slot=%d", api.PathGetLedgerDefinition, *slot)
	}
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}
	var resp api.LedgerDefinition
	if err = json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	if resp.Error.Error != "" {
		return nil, fmt.Errorf("server error: %s", resp.Error.Error)
	}
	return &resp, nil
}

// GetLedgerDefinitionYAML retrieves raw ledger definition YAML from server for the latest slot.
// This is a convenience method for backward compatibility.
func (c *APIClient) GetLedgerDefinitionYAML() ([]byte, error) {
	resp, err := c.GetLedgerDefinition(nil)
	if err != nil {
		return nil, err
	}
	return []byte(resp.LibraryYAML), nil
}

// getAccountOutputs fetches all outputs of the account. Optionally sorts them on the server
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

// GetChainOutput returns parsed output for the chain id
func (c *APIClient) GetChainOutput(chainID base.ChainID) (*ledger.OutputWithChainID, base.TransactionID, error) {
	oData, lrbid, err := c.GetChainOutputData(chainID)
	if err != nil {
		return nil, base.TransactionID{}, err
	}
	o, err := oData.ParseAsChainOutput()
	if err != nil {
		return nil, base.TransactionID{}, err
	}
	return o, lrbid, nil
}

func (c *APIClient) GetSequencerData(chainID base.ChainID) (ret seqdata.SequencerData, err error) {
	o, _, err := c.GetChainOutput(chainID)
	if err != nil {
		err = fmt.Errorf("GetSequencerData: error while retrieving UTXO for %s: %w", chainID.StringShort(), err)
		return
	}
	if !o.ID.IsSequencerTransaction() {
		err = fmt.Errorf("GetSequencerData: not a sequencer output: %s", chainID.StringShort())
	}
	return ledger.ParseSequencerData(o.Output)
}

// GetSequencerTargetInfo returns comprehensive sequencer info for delegators.
func (c *APIClient) GetSequencerTargetInfo(chainID base.ChainID) (*api.SequencerTargetInfo, error) {
	path := fmt.Sprintf(api.PathGetSequencerTargetInfo+"?chainid=%s", chainID.StringHex())
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}

	var res api.SequencerTargetInfo
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, err
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("GetSequencerTargetInfo for %s: %s", chainID.StringShort(), res.Error.Error)
	}
	return &res, nil
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

// GetOutputsParams carries the optional parameters of the unified
// `get_outputs` endpoint. Zero-value fields fall through to server
// defaults (max=200, sort_by=timestamp, sort_order=asc, lock_type=
// sigLock, no for_amount, chained=any). String defaults are signaled
// by the empty string; ForAmount == 0 means unset (any non-empty
// result satisfies a zero minimum); Chained == nil means no chained
// filter (return both chained and non-chained outputs).
type GetOutputsParams struct {
	MaxOutputs int
	SortBy     string
	SortOrder  string
	ForAmount  uint64
	LockType   string
	Chained    *bool
}

// ChainedOnly returns a *bool suitable for GetOutputsParams.Chained
// to filter results to chained outputs only.
func ChainedOnly() *bool { v := true; return &v }

// NonChainedOnly returns a *bool suitable for GetOutputsParams.Chained
// to filter results to non-chained outputs only.
func NonChainedOnly() *bool { v := false; return &v }

// GetOutputsResult is the parsed return shape of GetOutputs. Outputs
// are decoded with the latest ledger library; the API ships raw bytes.
type GetOutputsResult struct {
	Outputs         []*ledger.OutputWithID
	AvailableAmount uint64
	LimitExceeded   bool
	LRBID           base.TransactionID
}

// GetOutputs queries the unified state-query endpoint described in
// claude/get_outputs.md. indexValue is 1..255 raw bytes (the client
// hex-encodes for the URL). Output parsing requires the ledger
// library to be initialised in this process.
func (c *APIClient) GetOutputs(indexValue []byte, params ...GetOutputsParams) (*GetOutputsResult, error) {
	if len(indexValue) < 1 || len(indexValue) > 255 {
		return nil, fmt.Errorf("GetOutputs: indexValue must be 1..255 bytes, got %d", len(indexValue))
	}
	var p GetOutputsParams
	if len(params) > 0 {
		p = params[0]
	}
	path := fmt.Sprintf(api.PathGetOutputs+"?index_value=%s", hex.EncodeToString(indexValue))
	if p.MaxOutputs > 0 {
		path += fmt.Sprintf("&max_outputs=%d", p.MaxOutputs)
	}
	if p.SortBy != "" {
		path += "&sort_by=" + p.SortBy
	}
	if p.SortOrder != "" {
		path += "&sort_order=" + p.SortOrder
	}
	if p.ForAmount > 0 {
		path += fmt.Sprintf("&for_amount=%d", p.ForAmount)
	}
	if p.LockType != "" {
		path += "&lock_type=" + p.LockType
	}
	if p.Chained != nil {
		if *p.Chained {
			path += "&chained=true"
		} else {
			path += "&chained=false"
		}
	}

	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}
	var res api.GetOutputsResponse
	if err = json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("GetOutputs: unmarshal: %w; body: %s", err, string(body))
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("GetOutputs: from server: %s", res.Error.Error)
	}

	out := &GetOutputsResult{
		AvailableAmount: res.AvailableAmount,
		LimitExceeded:   res.LimitExceeded,
	}
	if res.LRBID != "" {
		out.LRBID, err = base.TransactionIDFromHexString(res.LRBID)
		if err != nil {
			return nil, fmt.Errorf("GetOutputs: invalid lrbid %s: %w", res.LRBID, err)
		}
	}
	out.Outputs = make([]*ledger.OutputWithID, 0, len(res.Outputs))
	for _, item := range res.Outputs {
		oid, err := base.OutputIDFromHexString(item.ID)
		if err != nil {
			return nil, fmt.Errorf("GetOutputs: invalid output id %s: %w", item.ID, err)
		}
		oData, err := hex.DecodeString(item.Data)
		if err != nil {
			return nil, fmt.Errorf("GetOutputs: invalid output data hex for %s: %w", item.ID, err)
		}
		o, err := ledger.OutputFromBytes(oData)
		if err != nil {
			return nil, fmt.Errorf("GetOutputs: parse output %s: %w", item.ID, err)
		}
		out.Outputs = append(out.Outputs, &ledger.OutputWithID{ID: oid, Output: o})
	}
	return out, nil
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
		o, err := ledger.OutputFromHexString(ci.Data)
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

// GetTransferableOutputs returns up to maxOutputs basic sigLock outputs
// (amounts | index-values | lock, no extras) owned by account, sorted
// descending by amount, plus the total balance.
func (c *APIClient) GetTransferableOutputs(account ledger.Controller, maxOutputs ...int) ([]*ledger.OutputWithID, *base.TransactionID, uint64, error) {
	maxO := 256
	if len(maxOutputs) > 0 && maxOutputs[0] < 256 && maxOutputs[0] > 0 {
		maxO = maxOutputs[0]
	}
	res, err := c.GetOutputs(account.ControllerID(), GetOutputsParams{
		LockType:   api.GetOutputsLockTypeSigLock,
		Chained:    NonChainedOnly(),
		SortBy:     api.GetOutputsSortByAmount,
		SortOrder:  api.GetOutputsSortOrderDesc,
		MaxOutputs: maxO,
	})
	if err != nil {
		return nil, nil, 0, err
	}
	if len(res.Outputs) == 0 {
		return nil, nil, 0, nil
	}
	// Restrict to "basic" outputs (no extras beyond amounts | index-values
	// | lock); these are unlockable with a plain signature/reference.
	ret := util.PurgeSlice(res.Outputs, func(o *ledger.OutputWithID) bool {
		return o.Output.NumElements() == 3
	})
	sum := uint64(0)
	for _, o := range ret {
		sum += o.Output.TokenBalance()
	}
	return ret, &res.LRBID, sum, nil
}

// SpendableOutputsParams controls GetSpendableOutputs filtering.
//
//   - IncludeSendWithDeadline = true (default behaviour at call sites that
//     want it) augments the basic sigLock set with sendWithDeadline UTXOs
//     the account can claim at TargetSlot:
//       * master == account AND TargetSlot − createSlot ≥ acceptanceSlots
//         (master-reclaim path), OR
//       * target == account AND TargetSlot − createSlot < acceptanceSlots
//         AND targetType == sigLock (target-accept path).
//   - chainLock-target acceptance paths are excluded because they need a
//     chain input in the same tx; that's a different flow than the simple
//     spend implied by GetSpendableOutputs.
//   - TargetSlot == 0 falls back to "now" (ledger.TimeNow().Slot()).
//
// All filtering is done client-side over a single GetOutputs call —
// no server changes required.
type SpendableOutputsParams struct {
	IncludeSendWithDeadline bool
	TargetSlot              uint32
	MaxOutputs              int
}

// GetSpendableOutputs returns outputs the account can spend at TargetSlot,
// optionally including sendWithDeadline UTXOs the account is currently
// claim-eligible for. The base behaviour mirrors GetTransferableOutputs.
func (c *APIClient) GetSpendableOutputs(account ledger.Controller, params SpendableOutputsParams) ([]*ledger.OutputWithID, *base.TransactionID, uint64, error) {
	maxO := params.MaxOutputs
	if maxO <= 0 || maxO > 256 {
		maxO = 256
	}
	if !params.IncludeSendWithDeadline {
		return c.GetTransferableOutputs(account, maxO)
	}

	targetSlot := params.TargetSlot
	if targetSlot == 0 {
		targetSlot = ledger.TimeNow().Slot
	}

	// One unfiltered query — the trie indexer returns any output whose
	// index-value tuple contains account.ControllerID(), so this picks up
	// sigLock and sendWithDeadline outputs (under either master or target)
	// in one round trip.
	res, err := c.GetOutputs(account.ControllerID(), GetOutputsParams{
		LockType:   api.GetOutputsLockTypeAll,
		Chained:    NonChainedOnly(),
		SortBy:     api.GetOutputsSortByAmount,
		SortOrder:  api.GetOutputsSortOrderDesc,
		MaxOutputs: maxO,
	})
	if err != nil {
		return nil, nil, 0, err
	}

	var (
		accountHID = account.ControllerID()
		sum        uint64
	)
	ret := make([]*ledger.OutputWithID, 0, len(res.Outputs))
	for _, o := range res.Outputs {
		if !c.spendableForAccount(o, accountHID, targetSlot) {
			continue
		}
		ret = append(ret, o)
		sum += o.Output.TokenBalance()
	}
	return ret, &res.LRBID, sum, nil
}

// spendableForAccount decides whether the given output is spendable by
// accountHID at targetSlot under a SINGLE-input signature unlock. Two
// shapes qualify:
//
//   - 3-element output (amounts | indexValues | lock) locked by sigLock
//     to accountHID — the legacy "transferable" case.
//   - sendWithDeadline output where accountHID is master AND has reached
//     the reclaim window, OR is the sigLock target AND we're still inside
//     the acceptance window.
//
// chainLock-target acceptance is excluded because the spend tx must also
// consume the controlling chain output (a separate flow).
func (c *APIClient) spendableForAccount(o *ledger.OutputWithID, accountHID []byte, targetSlot uint32) bool {
	if o == nil || o.Output == nil {
		return false
	}
	lock := o.Output.Lock()

	switch l := lock.(type) {
	case ledger.SigLock:
		// legacy "transferable" case: 3-element output owned by accountHID
		if o.Output.NumElements() != 3 {
			return false
		}
		return bytes.Equal(l[:], accountHID)
	case *ledger.SendWithDeadlineLock:
		createSlot := o.ID.Slot()
		if targetSlot < createSlot {
			return false
		}
		delta := targetSlot - createSlot
		if bytes.Equal(l.MasterID[:], accountHID) {
			return delta >= l.AcceptanceSlots // reclaim path (or public-cleanup overlap)
		}
		if bytes.Equal(l.TargetID[:], accountHID) && l.TargetType == ledger.SendWithDeadlineTargetSigLock {
			return delta < l.AcceptanceSlots // accept path (sigLock target only)
		}
		return false
	}
	return false
}

// MakeClaimingCompactTransaction is like MakeCompactTransaction, but the
// input set also includes consumable sendWithDeadline UTXOs — both
// master-reclaim (account is master, Δ ≥ acceptanceSlots) and target-
// accept (account is sigLock target, Δ < acceptanceSlots) paths — at
// the given targetSlot. The produced output is a single sigLock back
// to the wallet for the consolidated balance minus the tag-along fee.
//
// All inputs use the signature unlock (0xff) because:
//   - on a plain sigLock input it satisfies `equal($holder, txHolderID(txSignatureData))`.
//   - on a sendWithDeadline input the consumed-side dispatch lands in
//     `_sigLock($master)` (reclaim) or `_sigLock($target)` (accept);
//     both fall through `unlockedByReference` (which fails because the
//     SWD lock bytecode ≠ sigLock bytecode) onto the same signature
//     check, which matches the wallet's holderID.
//
// targetSlot == 0 falls back to ledger.TimeNow().Slot.
func (c *APIClient) MakeClaimingCompactTransaction(
	walletPrivateKey ed25519.PrivateKey,
	tagAlongSeqID *base.ChainID,
	tagAlongFee uint64,
	targetSlot uint32,
	maxInputs int,
) (*transaction.Transaction, error) {
	walletAccount := ledger.SigLockFromED25519PrivateKey(walletPrivateKey)

	walletOutputs, _, inTotal, err := c.GetSpendableOutputs(walletAccount, SpendableOutputsParams{
		IncludeSendWithDeadline: true,
		TargetSlot:              targetSlot,
		MaxOutputs:              maxInputs,
	})
	if err != nil {
		return nil, err
	}
	if len(walletOutputs) <= 1 {
		return nil, nil
	}
	if inTotal < tagAlongFee {
		return nil, fmt.Errorf("not enough balance for the tag-along fee")
	}

	nowisTs := ledger.TimeNow()
	if targetSlot != 0 {
		// Caller-controlled slot; use it for the tx timestamp so the
		// sendWithDeadline Δ checks line up with the filter.
		nowisTs = base.T(targetSlot, 1)
	}

	txb := txbuilder.New()
	for _, in := range walletOutputs {
		_, err := txb.ConsumeOutput(in.Output, in.ID)
		if err != nil {
			return nil, fmt.Errorf("MakeClaimingCompactTransaction: consume: %w", err)
		}
	}
	// Signature unlock on EVERY input (see method comment for why this
	// works uniformly for sigLock and sendWithDeadline locks claimed by
	// the wallet).
	for i := range walletOutputs {
		txb.PutSignatureUnlock(byte(i))
	}

	// Combined output back to the wallet.
	mainAmount := inTotal - tagAlongFee
	mainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(mainAmount).WithLock(walletAccount)
	})
	if _, err = txb.ProduceOutput(mainOut); err != nil {
		return nil, err
	}

	if tagAlongFee > 0 {
		if tagAlongSeqID == nil {
			return nil, fmt.Errorf("tag-along sequencer not specified")
		}
		taOut := ledger.NewTagAlongOutput(tagAlongFee, *tagAlongSeqID, base.HolderID(walletAccount))
		if _, err = txb.ProduceOutput(taOut); err != nil {
			return nil, err
		}
	}

	txb.TransactionData.Timestamp = nowisTs
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(walletPrivateKey)

	txBytes, _, _, err := txb.BytesWithValidation()
	if err != nil {
		return nil, err
	}
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil {
		return tx, err
	}
	if err = tx.SetFullContext(tx.InputLoaderByIndex(transaction.PickOutputFromListFunc(walletOutputs))); err != nil {
		return tx, err
	}
	return tx, nil
}

// MakeCompactTransaction requests server and creates a compact transaction for ED25519 outputs in the form of transaction context. Does not submit it
func (c *APIClient) MakeCompactTransaction(walletPrivateKey ed25519.PrivateKey, tagAlongSeqID *base.ChainID, tagAlongFee uint64, maxInputs ...int) (*transaction.Transaction, error) {
	walletAccount := ledger.SigLockFromED25519PrivateKey(walletPrivateKey)

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

	tx, err := transaction.ParseWithPartialValidation(txBytes)
	if err != nil {
		return tx, err
	}
	err = tx.SetFullContext(tx.InputLoaderByIndex(transaction.PickOutputFromListFunc(walletOutputs)))
	if err != nil {
		return tx, err
	}
	return tx, nil
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

func (c *APIClient) TransferFromED25519Wallet(par TransferFromED25519WalletParams) (*transaction.Transaction, error) {
	if par.Amount < minimumTransferAmount {
		return nil, fmt.Errorf("minimum transfer amount is %d", minimumTransferAmount)
	}
	walletAccount := ledger.SigLockFromED25519PrivateKey(par.WalletPrivateKey)
	needed := par.Amount + par.TagAlongFee
	res, err := c.GetOutputs(walletAccount.ControllerID(), GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   NonChainedOnly(),
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

func (c *APIClient) MakeChainOrigin(par TransferFromED25519WalletParams) (*transaction.Transaction, base.ChainID, error) {
	if par.Amount < minimumTransferAmount {
		return nil, base.NilChainID, fmt.Errorf("minimum transfer amount is %d", minimumTransferAmount)
	}

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
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(par.WalletPrivateKey)

	txBytes := txb.TransactionData.Bytes()

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

// GetLatestReliableBranch retrieves latest reliable branch info from the node.
// The returned BranchDataJSONAble carries Root + SequencerID + the
// stem-projected aggregates (Supply, CoverageDelta, etc.).
func (c *APIClient) GetLatestReliableBranch() (*multistate.BranchDataJSONAble, base.TransactionID, error) {
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
	return &res.BranchData, res.BranchID, nil
}

// GetBranchListAfter returns branch IDs on the source's main chain after the given branch.
// Returns an error if the branch is not in the source's chain (different fork).
func (c *APIClient) GetBranchListAfter(afterBranch base.TransactionID, max int) ([]base.TransactionID, uint32, error) {
	path := fmt.Sprintf("%s?after_branch=%s&max=%d", api.PathGetBranchList, afterBranch.StringHex(), max)
	return c.parseBranchListResponse(path)
}

// GetBranchListFromSlot returns branch IDs on the main chain forward from fromSlot.
// No fork detection — use GetBranchListAfter when fork safety is needed.
func (c *APIClient) GetBranchListFromSlot(fromSlot uint32, max int) ([]base.TransactionID, uint32, error) {
	path := fmt.Sprintf("%s?from_slot=%d&max=%d", api.PathGetBranchList, fromSlot, max)
	return c.parseBranchListResponse(path)
}

func (c *APIClient) parseBranchListResponse(path string) ([]base.TransactionID, uint32, error) {
	body, err := c.getBody(path)
	if err != nil {
		return nil, 0, err
	}
	var res api.BranchList
	if err = json.Unmarshal(body, &res); err != nil {
		return nil, 0, fmt.Errorf("unmarshal returned: %v\nbody: '%s'", err, string(body))
	}
	if res.Error.Error != "" {
		return nil, 0, fmt.Errorf("from server: %s", res.Error.Error)
	}
	ret := make([]base.TransactionID, 0, len(res.Branches))
	for _, hexID := range res.Branches {
		txid, err := base.TransactionIDFromHexString(hexID)
		if err != nil {
			return nil, 0, fmt.Errorf("invalid branch ID '%s': %v", hexID, err)
		}
		ret = append(ret, txid)
	}
	return ret, res.LRBSlot, nil
}

// GetSnapshotInfo returns metadata about the latest snapshot on the remote host.
func (c *APIClient) GetSnapshotInfo() (*api.SnapshotInfo, error) {
	body, err := c.getBody(api.PathGetSnapshotInfo)
	if err != nil {
		return nil, err
	}
	var res api.SnapshotInfo
	if err = json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("unmarshal returned: %v", err)
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}
	return &res, nil
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

	txb.TransactionData.Timestamp = par.Timestamp
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(par.PrivateKey)

	txBytes, _, txString, err := txb.BytesWithValidation()

	if err != nil {
		err = fmt.Errorf("%v\n------ failing transaction -------\n%s", err, txString)
	}

	return txBytes, err
}

// TxLogEnable enables or disables the transaction logger with the specified level.
// Level values: "off", "branch", "sequencer", "non_sequencer", "all"
func (c *APIClient) TxLogEnable(level string) (*api.TxLogEnableResponse, error) {
	url := fmt.Sprintf("%s%s?level=%s", c.prefix, api.PathTxLogEnable, level)
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var res api.TxLogEnableResponse
	if err = json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("server error: %s", res.Error.Error)
	}
	return &res, nil
}

// TxLogGet retrieves log records by transaction ID prefix (hex-encoded).
func (c *APIClient) TxLogGet(prefixHex string, max int) (*api.TxLogResponse, error) {
	path := fmt.Sprintf("%s?prefix=%s", api.PathTxLogGet, prefixHex)
	if max > 0 {
		path += fmt.Sprintf("&max=%d", max)
	}
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}

	var res api.TxLogResponse
	if err = json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("server error: %s", res.Error.Error)
	}
	return &res, nil
}

// TxLogStatus retrieves the current transaction logger status.
func (c *APIClient) TxLogStatus() (*api.TxLogEnableResponse, error) {
	body, err := c.getBody(api.PathTxLogStatus)
	if err != nil {
		return nil, err
	}

	var res api.TxLogEnableResponse
	if err = json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("server error: %s", res.Error.Error)
	}
	return &res, nil
}

// TxLogRange retrieves log records within a time range (Unix nanoseconds).
func (c *APIClient) TxLogRange(fromNs, toNs int64, max int) (*api.TxLogResponse, error) {
	path := fmt.Sprintf("%s?from=%d", api.PathTxLogRange, fromNs)
	if toNs > 0 {
		path += fmt.Sprintf("&to=%d", toNs)
	}
	if max > 0 {
		path += fmt.Sprintf("&max=%d", max)
	}
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}

	var res api.TxLogResponse
	if err = json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("server error: %s", res.Error.Error)
	}
	return &res, nil
}

func (c *APIClient) MakeSendOutputTransaction(o *ledger.Output, privateKey ed25519.PrivateKey, ts base.LedgerTime) ([]byte, base.TransactionID, string, error) {
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
		o, err := ledger.OutputFromHexString(data.Data)
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

// DownloadSnapshot downloads the latest snapshot file from the node.
// If destPath is non-empty, saves to that path. Otherwise, uses the filename from the server response.
// Returns the path of the saved file.
// Use command 'wget --content-disposition http://<ip addr>>:<API port>/api/v1/get_snapshot'
// to download snapshot file with original name
func (c *APIClient) DownloadSnapshot(destPath string) (string, error) {
	url := c.prefix + api.PathGetSnapshot

	// Use a separate client with no timeout for large file downloads
	downloadClient := http.Client{
		Transport: c.c.Transport,
	}
	resp, err := downloadClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("snapshot download request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		var apiErr api.Error
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error != "" {
			return "", fmt.Errorf("from server: %s", apiErr.Error)
		}
		return "", fmt.Errorf("server returned status %d", resp.StatusCode)
	}

	// extract filename from Content-Disposition header
	headerFilename := ""
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if fn, ok := params["filename"]; ok && fn != "" {
				headerFilename = fn
			}
		}
	}

	if destPath == "" {
		destPath = headerFilename
		if destPath == "" {
			destPath = "downloaded.snapshot"
		}
	} else if info, err := os.Stat(destPath); err == nil && info.IsDir() {
		// destPath is a directory — place the file inside it
		fn := headerFilename
		if fn == "" {
			fn = "downloaded.snapshot"
		}
		destPath = filepath.Join(destPath, fn)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("cannot create file '%s': %w", destPath, err)
	}

	_, err = io.Copy(f, resp.Body)
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(destPath)
		return "", fmt.Errorf("failed to save snapshot: %w", err)
	}

	return destPath, nil
}
