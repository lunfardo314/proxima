package client

import (
	"bytes"
	"context"
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
	"sync"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core/core_modules/tippool"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
)

const apiDefaultClientTimeout = 7 * time.Second

type APIClient struct {
	c      http.Client
	prefix string

	// walletLib is a lazily-fetched txbuildercore.Library used to parse
	// chain constraints (and any other bytecode-level structures the
	// client needs to surface to callers) WITHOUT touching the global
	// ledger.L() singleton. The wasm-style proxi wallet design forbids
	// the singleton; this field is how the client honours that — it
	// pulls the same library JSON the wallet would, on first need,
	// and caches it for the client's lifetime.
	walletLibOnce sync.Once
	walletLib     *txbuildercore.Library[any]
	walletLibErr  error
}

// walletLibrary returns the lazily-fetched wallet-side library bound to
// this client. Used internally to parse on-wire output bytes (chain
// constraints in particular) without depending on the ledger.L() singleton.
func (c *APIClient) walletLibrary() (*txbuildercore.Library[any], error) {
	c.walletLibOnce.Do(func() {
		c.walletLib, c.walletLibErr = c.GetLibrary(nil)
	})
	return c.walletLib, c.walletLibErr
}

// parseAsChainOutput wraps raw output bytes + ID into an OutputWithChainID
// using the client's wallet library (singleton-free). The chain constraint
// is read via lib.ParseChainConstraint; for chain-origin outputs the
// ChainID is resolved as blake2b(outputID) to match the constraint's
// post-origin enforcement.
func (c *APIClient) parseAsChainOutput(oData ledger.OutputDataWithID) (*ledger.OutputWithChainID, error) {
	o, err := ledger.OutputFromBytes(oData.Data)
	if err != nil {
		return nil, fmt.Errorf("parseAsChainOutput: %w", err)
	}
	lib, err := c.walletLibrary()
	if err != nil {
		return nil, fmt.Errorf("parseAsChainOutput: wallet library: %w", err)
	}
	cc, err := parseChainConstraintFromOutput(o, lib)
	if err != nil {
		return nil, fmt.Errorf("parseAsChainOutput: %w", err)
	}
	resolvedChainID := cc.ChainID
	if resolvedChainID == base.NilChainID {
		resolvedChainID = base.MakeOriginChainID(oData.ID)
	}
	return &ledger.OutputWithChainID{
		OutputWithID:        ledger.OutputWithID{Output: o, ID: oData.ID},
		ChainConstraintData: chainConstraintDataFromView(cc, resolvedChainID),
	}, nil
}

// parseAsSequencerOutput wraps raw output bytes + ID into an
// OutputWithSequencerData using the client's wallet library
// (singleton-free). Returns ok=false if the output is not a sequencer
// output (no chain constraint or no sequencer constraint at extras).
func (c *APIClient) parseAsSequencerOutput(oData ledger.OutputDataWithID) (*ledger.OutputWithSequencerData, error) {
	o, err := ledger.OutputFromBytes(oData.Data)
	if err != nil {
		return nil, fmt.Errorf("parseAsSequencerOutput: %w", err)
	}
	lib, err := c.walletLibrary()
	if err != nil {
		return nil, fmt.Errorf("parseAsSequencerOutput: wallet library: %w", err)
	}
	cc, err := parseChainConstraintFromOutput(o, lib)
	if err != nil {
		return nil, fmt.Errorf("parseAsSequencerOutput: %w", err)
	}
	resolvedChainID := cc.ChainID
	if resolvedChainID == base.NilChainID {
		resolvedChainID = base.MakeOriginChainID(oData.ID)
	}
	// Sequencer-output sanity: must carry the sequencer constraint at the
	// fixed slot. Parse it via the wallet library so the (epochSlots,
	// maxFrozenEpochs, coverageDelta) values are surfaced to callers.
	seqBin, err := o.ConstraintAt(ledger.SequencerConstraintFixedIndex)
	if err != nil || len(seqBin) == 0 {
		return nil, fmt.Errorf("parseAsSequencerOutput: not a sequencer output: %s", oData.ID.String())
	}
	seqView, err := lib.ParseSequencerConstraint(seqBin)
	if err != nil {
		return nil, fmt.Errorf("parseAsSequencerOutput: %w", err)
	}
	// Sequencer milestone data is a singleton-free byte parse off the
	// inline-data constraint at SeqMilestoneDataFixedIndex.
	var seqData *seqdata.SequencerData
	if sd, err := ledger.ParseSequencerData(o); err == nil {
		seqData = &sd
	}
	ccData := chainConstraintDataFromView(cc, resolvedChainID)
	return &ledger.OutputWithSequencerData{
		OutputWithID: ledger.OutputWithID{Output: o, ID: oData.ID},
		SequencerOutputData: ledger.SequencerOutputData{
			SequencerConstraint: ledger.NewSequencerConstraint(seqView.EpochSlots, seqView.MaxFrozenEpochs, seqView.CoverageDelta),
			ChainConstraint:     &ccData.ChainConstraint,
			AmountOnChain:       o.TokenBalance(),
			SequencerData:       seqData,
		},
	}, nil
}

// parseChainConstraintFromOutput is the shared chain-constraint byte-parse
// used by parseAsChainOutput and parseAsSequencerOutput. Returns an error
// if the output has no constraint at the chain slot.
func parseChainConstraintFromOutput(o *ledger.Output, lib *txbuildercore.Library[any]) (*txbuildercore.ChainConstraintView, error) {
	chainBin, err := o.ConstraintAt(ledger.ConstraintIndexChain)
	if err != nil || len(chainBin) == 0 {
		return nil, fmt.Errorf("no chain constraint at index %d", ledger.ConstraintIndexChain)
	}
	return lib.ParseChainConstraint(chainBin)
}

// chainConstraintDataFromView projects a wallet-side ChainConstraintView
// into the singleton-coupled ledger.ChainConstraintData shape that
// OutputWithChainID / OutputWithSequencerData carry on the wire-facing
// types. Pure field copy; ChainID is taken from the resolved value (origin
// outputs collapse NilChainID → blake2b(outputID) at the call site).
func chainConstraintDataFromView(cc *txbuildercore.ChainConstraintView, resolvedChainID base.ChainID) ledger.ChainConstraintData {
	return ledger.ChainConstraintData{
		ChainConstraint: ledger.ChainConstraint{
			ChainID:                  resolvedChainID,
			PredecessorInputIndex:    cc.PredecessorInputIndex,
			OriginSlot:               cc.OriginSlot,
			CumulativeChainInflation: cc.CumulativeChainInflation,
			CumulativeBranchBonus:    cc.CumulativeBranchBonus,
			TransitionCounter:        cc.TransitionCounter,
			BranchCounter:            cc.BranchCounter,
		},
	}
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

// GetLedgerDefinitionJSON retrieves raw ledger definition JSON from server for the latest slot.
func (c *APIClient) GetLedgerDefinitionJSON() ([]byte, error) {
	resp, err := c.GetLedgerDefinition(nil)
	if err != nil {
		return nil, err
	}
	return []byte(resp.LibraryJSON), nil
}

// GetLibrary fetches the ledger library descriptor for the given slot
// (latest if slot is nil) and constructs a wallet-side
// *txbuildercore.Library ready for composing transactions. Does NOT
// touch the ledger.L() singleton — the wallet caller owns the returned
// library instance.
func (c *APIClient) GetLibrary(slot *uint32) (*txbuildercore.Library[any], error) {
	resp, err := c.GetLedgerDefinition(slot)
	if err != nil {
		return nil, err
	}
	desc, err := easyfl.ReadLibraryFromJSON([]byte(resp.LibraryJSON))
	if err != nil {
		return nil, fmt.Errorf("parse library JSON: %w", err)
	}
	lib, err := txbuildercore.NewLibrary(desc)
	if err != nil {
		return nil, fmt.Errorf("build txbuildercore.Library: %w", err)
	}
	return lib, nil
}

// GetLedgerConstants fetches the runtime ledger constants extracted
// from the library active at the given slot (latest if slot is nil).
// Returns a wallet-side *txbuildercore.Constants. The wallet does NOT
// need the ledger.L() singleton to use these.
//
// On the wire the response is the JSON-marshalled Constants directly.
// To detect the error-envelope shape ({"error": "..."}) emitted by
// api.WriteErr on server-side parameter validation failures, the
// payload is parsed once via a peek-then-decode pattern.
func (c *APIClient) GetLedgerConstants(slot *uint32) (*txbuildercore.Constants, error) {
	path := api.PathGetLedgerConstants
	if slot != nil {
		path = fmt.Sprintf("%s?slot=%d", api.PathGetLedgerConstants, *slot)
	}
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}
	// Probe for an error envelope first.
	var probe struct {
		Error string `json:"error"`
	}
	if err = json.Unmarshal(body, &probe); err == nil && probe.Error != "" {
		return nil, fmt.Errorf("server error: %s", probe.Error)
	}
	var consts txbuildercore.Constants
	if err = json.Unmarshal(body, &consts); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	return &consts, nil
}

// GetLedgerTime returns the node's current ledger time. Use it as the
// transaction timestamp directly instead of converting wall-clock time
// client-side.
func (c *APIClient) GetLedgerTime() (base.LedgerTime, error) {
	body, err := c.getBody(api.PathGetLedgerTime)
	if err != nil {
		return base.NilLedgerTime, err
	}
	var resp api.LedgerTimeNow
	if err = json.Unmarshal(body, &resp); err != nil {
		return base.NilLedgerTime, fmt.Errorf("failed to unmarshal response: %w", err)
	}
	if resp.Error.Error != "" {
		return base.NilLedgerTime, fmt.Errorf("server error: %s", resp.Error.Error)
	}
	return base.T(resp.Slot, resp.Tick), nil
}

// EvalResult is the in-process form of one entry in the /api/v1/eval
// response. Value carries the raw evaluation bytes (hex-decoded);
// Error is the server-side per-formula failure message. Exactly one
// of the two is non-zero per entry.
type EvalResult struct {
	Value []byte
	Error string
}

// Eval batches a list of closed EasyFL formulas to the server's
// /api/v1/eval endpoint and returns one EvalResult per source in
// input order. slot=0 means "latest at request time" (server-side
// MaxSlot default).
//
// Per-formula compile / eval failures do NOT fail the batch; they
// land in EvalResult.Error. Returns a non-nil error only on transport
// or response-decoding failure.
func (c *APIClient) Eval(slot uint32, sources []string) ([]EvalResult, error) {
	reqBytes, err := json.Marshal(&api.EvalRequest{Slot: slot, Sources: sources})
	if err != nil {
		return nil, fmt.Errorf("marshal eval request: %w", err)
	}
	url := c.prefix + api.PathEval
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.c.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var raw api.EvalResponse
	if err = json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode eval response: %w (body=%s)", err, string(body))
	}
	if raw.Error.Error != "" {
		return nil, fmt.Errorf("server error: %s", raw.Error.Error)
	}
	out := make([]EvalResult, len(raw.Results))
	for i, r := range raw.Results {
		if r.Error != "" {
			out[i].Error = r.Error
			continue
		}
		bin, decErr := hex.DecodeString(r.Value)
		if decErr != nil {
			return nil, fmt.Errorf("decode results[%d].value hex: %w", i, decErr)
		}
		out[i].Value = bin
	}
	return out, nil
}

// EvalU64 is the single-formula uint64 convenience wrapper around
// Eval. Useful for "give me the value of <constName>" calls.
// Returns the per-formula error verbatim when the server reports one.
func (c *APIClient) EvalU64(slot uint32, source string) (uint64, error) {
	results, err := c.Eval(slot, []string{source})
	if err != nil {
		return 0, err
	}
	if len(results) != 1 {
		return 0, fmt.Errorf("EvalU64: expected 1 result, got %d", len(results))
	}
	if results[0].Error != "" {
		return 0, fmt.Errorf("eval %q: %s", source, results[0].Error)
	}
	return easyfl_util.Uint64FromBytes(results[0].Value)
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

// GetChainOutput returns parsed output for the chain id. Singleton-free:
// the chain constraint is parsed via the client's wallet library.
func (c *APIClient) GetChainOutput(chainID base.ChainID) (*ledger.OutputWithChainID, base.TransactionID, error) {
	oData, lrbid, err := c.GetChainOutputData(chainID)
	if err != nil {
		return nil, base.TransactionID{}, err
	}
	o, err := c.parseAsChainOutput(*oData)
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

// SubmitOption tunes a SubmitTransactionWithDetail call.
type SubmitOption func(*api.SubmitTxRequest)

// WithConsumedUTXOs supplies hex-encoded raw output bytes for each
// consumed input (positional match to the tx's InputIDs). When
// supplied, the server runs full-context validation before submit.
func WithConsumedUTXOs(consumed [][]byte) SubmitOption {
	return func(req *api.SubmitTxRequest) {
		hexed := make([]string, len(consumed))
		for i, b := range consumed {
			hexed[i] = hex.EncodeToString(b)
		}
		req.ConsumedUTXOs = hexed
	}
}

// WithValidateOnly makes the server run validation stages only and
// skip enqueueing the transaction into the workflow.
func WithValidateOnly() SubmitOption {
	return func(req *api.SubmitTxRequest) {
		req.ValidateOnly = true
	}
}

// SubmitTransaction posts the tx bytes to /api/v1/submit_tx with no
// additional validation options. Returns nil on success, or a
// "from server (stage=...): ..." error on any failure. Backward-
// compatible wrapper used by the legacy proxi call sites.
func (c *APIClient) SubmitTransaction(txBytes []byte) error {
	_, err := c.SubmitTransactionWithDetail(txBytes)
	return err
}

// SubmitTransactionWithDetail posts the tx bytes to /api/v1/submit_tx
// with optional SubmitOption modifiers (consumed_utxos for full-
// context validation, validate_only for dry-run). On success returns
// the parsed transaction ID. On failure returns a "from server
// (stage=...): ..." error.
func (c *APIClient) SubmitTransactionWithDetail(txBytes []byte, opts ...SubmitOption) (base.TransactionID, error) {
	reqBody := api.SubmitTxRequest{
		TxBytes: hex.EncodeToString(txBytes),
	}
	for _, opt := range opts {
		opt(&reqBody)
	}
	reqBytes, err := json.Marshal(&reqBody)
	if err != nil {
		return base.TransactionID{}, fmt.Errorf("marshal submit request: %w", err)
	}

	url := c.prefix + api.PathSubmitTransaction
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(reqBytes))
	if err != nil {
		return base.TransactionID{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.c.Do(httpReq)
	if err != nil {
		return base.TransactionID{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return base.TransactionID{}, err
	}

	var res api.SubmitTxResponse
	if err = json.Unmarshal(body, &res); err != nil {
		return base.TransactionID{}, fmt.Errorf("decode submit response: %w (body=%s)", err, string(body))
	}
	if !res.OK {
		return base.TransactionID{}, fmt.Errorf("from server (stage=%s): %s", res.Stage, res.Error)
	}
	return base.TransactionIDFromHexString(res.TxID)
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
	// Spendable=true post-filters the result set to outputs claimable
	// by `indexValue` (treated as the wallet's holder ID) under a
	// single-input signature unlock at TargetSlot. Used by
	// GetSpendableOutputs.
	Spendable bool
	// TargetSlot is consulted only when Spendable is true; it selects
	// the slot for the sendWithDeadline Δ check AND the library
	// version that dispatches the lock. 0 → server's current LRB slot.
	TargetSlot uint32
}

// ChainedOnly returns a *bool suitable for GetOutputsParams.Chained
// to filter results to chained outputs only.
func ChainedOnly() *bool { v := true; return &v }

// NonChainedOnly returns a *bool suitable for GetOutputsParams.Chained
// to filter results to non-chained outputs only.
func NonChainedOnly() *bool { v := false; return &v }

// GetOutputsResult is the parsed return shape of
// GetOutputsForControllerID. Outputs are structurally parsed
// (txbuildercore.OutputFromBytes — no ledger library required);
// methods that dispatch on lock kind (Output.Lock, etc.) still need
// the ledger singleton at the caller.
type GetOutputsResult struct {
	Outputs         []*ledger.OutputWithID
	AvailableAmount uint64
	LimitExceeded   bool
	LRBID           base.TransactionID
}

// GetOutputsForControllerID queries the unified state-query endpoint
// described in claude/get_outputs.md. indexValue is 1..255 raw bytes
// (the client hex-encodes for the URL); typically the wallet's
// holder ID (sigLock / chainLock / delegateLock / tagAlongLock
// controller). Output bytes returned by the server are structurally
// parsed here (no ledger singleton required).
func (c *APIClient) GetOutputsForControllerID(indexValue []byte, params ...GetOutputsParams) (*GetOutputsResult, error) {
	if len(indexValue) < 1 || len(indexValue) > 255 {
		return nil, fmt.Errorf("GetOutputsForControllerID: indexValue must be 1..255 bytes, got %d", len(indexValue))
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
	if p.Spendable {
		path += "&spendable=true"
	}
	if p.TargetSlot > 0 {
		path += fmt.Sprintf("&target_slot=%d", p.TargetSlot)
	}

	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}
	var res api.GetOutputsResponse
	if err = json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("GetOutputsForControllerID: unmarshal: %w; body: %s", err, string(body))
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("GetOutputsForControllerID: from server: %s", res.Error.Error)
	}

	out := &GetOutputsResult{
		AvailableAmount: res.AvailableAmount,
		LimitExceeded:   res.LimitExceeded,
	}
	if res.LRBID != "" {
		out.LRBID, err = base.TransactionIDFromHexString(res.LRBID)
		if err != nil {
			return nil, fmt.Errorf("GetOutputsForControllerID: invalid lrbid %s: %w", res.LRBID, err)
		}
	}
	out.Outputs = make([]*ledger.OutputWithID, 0, len(res.Outputs))
	for _, item := range res.Outputs {
		oid, err := base.OutputIDFromHexString(item.ID)
		if err != nil {
			return nil, fmt.Errorf("GetOutputsForControllerID: invalid output id %s: %w", item.ID, err)
		}
		oData, err := hex.DecodeString(item.Data)
		if err != nil {
			return nil, fmt.Errorf("GetOutputsForControllerID: invalid output data hex for %s: %w", item.ID, err)
		}
		o, err := ledger.OutputFromBytes(oData)
		if err != nil {
			return nil, fmt.Errorf("GetOutputsForControllerID: parse output %s: %w", item.ID, err)
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
	// Parse each chain output via the wallet library — no ledger.L()
	// singleton. The map key carries the chainID over the wire but we
	// still re-derive it from the constraint so callers see the full
	// chain-constraint metadata (cumulative inflation, counters, etc.),
	// matching what ledger.AsOutputWithChainID used to return.
	for chainIDHex, ci := range res.Chains {
		oid, err := base.OutputIDFromHexString(ci.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("GetAllChains: outputID for %s: %w", chainIDHex, err)
		}
		oData, err := hex.DecodeString(ci.Data)
		if err != nil {
			return nil, nil, fmt.Errorf("GetAllChains: output data for %s: %w", chainIDHex, err)
		}
		parsed, err := c.parseAsChainOutput(ledger.OutputDataWithID{ID: oid, Data: oData})
		if err != nil {
			return nil, nil, fmt.Errorf("GetAllChains: %w", err)
		}
		ret = append(ret, parsed)
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
	res, err := c.GetOutputsForControllerID(account.ControllerID(), GetOutputsParams{
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
//   - IncludeConditionalLocks = true augments the basic sigLock set with
//     window-dependent UTXOs the account can claim at TargetSlot:
//   - sendWithDeadline where master == account AND
//     TargetSlot − createSlot ≥ acceptanceSlots (master-reclaim path), OR
//     target == account AND TargetSlot − createSlot < acceptanceSlots
//     AND targetType == sigLock (target-accept path);
//   - tagAlong where sender == account AND
//     TargetSlot − createSlot ≥ tag_along_slots (sender-reclaim path).
//   - chainLock-target acceptance and the tagAlong target side are
//     excluded because they need a chain input in the same tx; that's a
//     different flow than the simple spend implied by
//     GetSpendableOutputs.
//   - TargetSlot == 0 → server defaults to its current LRB slot.
//
// Filtering happens server-side over a single GetOutputsForControllerID
// call (`spendable=true` + `target_slot=N`). The server uses the
// library active at target_slot for lock dispatch.
type SpendableOutputsParams struct {
	IncludeConditionalLocks bool
	TargetSlot              uint32
	MaxOutputs              int
}

// GetSpendableOutputs returns outputs the controller (siglock or chainlock) can spend at
// TargetSlot, optionally including the window-dependent UTXOs the
// account is currently claim-eligible for. The base behaviour mirrors
// GetTransferableOutputs.
//
// Server-side filtering: this is a thin wrapper around the unified
// get_outputs endpoint with `spendable=true` + `target_slot=N`. The
// server applies the spendable filter at the requested slot using the
// library version active at that slot. No singleton dependency on the
// client side.
func (c *APIClient) GetSpendableOutputs(controller ledger.Controller, params SpendableOutputsParams) ([]*ledger.OutputWithID, *base.TransactionID, uint64, error) {
	maxO := params.MaxOutputs
	if maxO <= 0 || maxO > 256 {
		maxO = 256
	}
	if !params.IncludeConditionalLocks {
		return c.GetTransferableOutputs(controller, maxO)
	}
	res, err := c.GetOutputsForControllerID(controller.ControllerID(), GetOutputsParams{
		LockType:   api.GetOutputsLockTypeAll,
		Chained:    NonChainedOnly(),
		SortBy:     api.GetOutputsSortByAmount,
		SortOrder:  api.GetOutputsSortOrderDesc,
		MaxOutputs: maxO,
		Spendable:  true,
		TargetSlot: params.TargetSlot,
	})
	if err != nil {
		return nil, nil, 0, err
	}
	return res.Outputs, &res.LRBID, res.AvailableAmount, nil
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

// GetBranchChainTo returns the back-chain (oldest-first) of toBranch — its OWN lineage — with slot
// > fromSlot, capped server-side (oldest entries kept). The source walks back from toBranch itself,
// so the returned chain is guaranteed on toBranch's lineage. Returns an error if the source does not
// know toBranch (it is on a fork the source lacks). Forward sync uses this both to probe for the
// latest common branch and to fetch the chain to commit.
func (c *APIClient) GetBranchChainTo(toBranch base.TransactionID, fromSlot uint32) ([]base.TransactionID, uint32, error) {
	path := fmt.Sprintf("%s?to_branch=%s&from_slot=%d", api.PathGetBranchList, toBranch.StringHex(), fromSlot)
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
	return ret, res.TopSlot, nil
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

func (c *APIClient) GetEarliestBranchIDs() (ret []base.TransactionID, err error) {
	body, err := c.getBody(api.PathGetEarliestBranchIDs)
	if err != nil {
		return
	}
	var res api.EarliestBranchIDs
	err = json.Unmarshal(body, &res)
	if err != nil {
		err = fmt.Errorf("unmarshal returned: %v\nbody: '%s'", err, string(body))
		return
	}
	if res.Error.Error != "" {
		err = fmt.Errorf("from server: %s", res.Error.Error)
		return
	}
	for _, s := range res.IDs {
		id, err2 := base.TransactionIDFromHexString(s)
		if err2 != nil {
			return nil, err2
		}
		ret = append(ret, id)
	}
	return
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
		oData, err := hex.DecodeString(data.Data)
		if err != nil {
			return nil, nil, err
		}
		parsed, err := c.parseAsSequencerOutput(ledger.OutputDataWithID{ID: seqOutID, Data: oData})
		if err != nil {
			return nil, nil, fmt.Errorf("GetAllSequencerOutputs: %w", err)
		}
		if seqID != parsed.SequencerOutputData.ChainConstraint.ChainID {
			return nil, nil, fmt.Errorf("inconsistency: chain IDs do not match (server: %s, parsed: %s)",
				seqID.String(), parsed.SequencerOutputData.ChainConstraint.ChainID.String())
		}
		ret[seqID] = *parsed
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

// CleanableOutputsParams controls one get_cleanable_outputs scan.
type CleanableOutputsParams struct {
	// FromChunk is the slot chunk to start scanning down from. Zero means
	// "let the server start at the newest chunk that can hold public dust".
	// Carry NextChunk over from the previous result to avoid re-walking the
	// clean tail.
	FromChunk uint32
	// FromChunkSet distinguishes "start at chunk 0" from "unset".
	FromChunkSet bool
	// MaxOutputs caps one scan; the server cuts as soon as it has this many.
	MaxOutputs int
}

// CleanableOutputsResult is one bite of publicly-claimable dust.
type CleanableOutputsResult struct {
	Outputs   []*ledger.OutputWithID
	NextChunk uint32
	Exhausted bool
	// NeedsReturn counts dust skipped because it carries returnToSender and
	// can only be taken against a return receipt to the master.
	NeedsReturn int
	LRBID       base.TransactionID
}

// GetCleanableOutputs asks the node to scan old state for outputs that have
// decayed into the public window of their conditional lock, where any signer
// may consume them. The scan is slot-chunked and cut short at MaxOutputs, so
// each call is cheap; a cleaner loops, spending each bite as one transaction.
func (c *APIClient) GetCleanableOutputs(params ...CleanableOutputsParams) (*CleanableOutputsResult, error) {
	var p CleanableOutputsParams
	if len(params) > 0 {
		p = params[0]
	}
	path := api.PathGetCleanableOutputs + "?"
	if p.FromChunkSet {
		path += fmt.Sprintf("from_chunk=%d&", p.FromChunk)
	}
	if p.MaxOutputs > 0 {
		path += fmt.Sprintf("max_outputs=%d", p.MaxOutputs)
	}

	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}
	var res api.GetCleanableOutputsResponse
	if err = json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("GetCleanableOutputs: unmarshal: %w; body: %s", err, string(body))
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("GetCleanableOutputs: from server: %s", res.Error.Error)
	}

	ret := &CleanableOutputsResult{
		NextChunk:   res.NextChunk,
		Exhausted:   res.Exhausted,
		NeedsReturn: res.NeedsReturn,
	}
	if res.LRBID != "" {
		if ret.LRBID, err = base.TransactionIDFromHexString(res.LRBID); err != nil {
			return nil, fmt.Errorf("GetCleanableOutputs: invalid lrbid %s: %w", res.LRBID, err)
		}
	}
	ret.Outputs = make([]*ledger.OutputWithID, 0, len(res.Outputs))
	for _, item := range res.Outputs {
		oid, err := base.OutputIDFromHexString(item.ID)
		if err != nil {
			return nil, fmt.Errorf("GetCleanableOutputs: invalid output id %s: %w", item.ID, err)
		}
		oData, err := hex.DecodeString(item.Data)
		if err != nil {
			return nil, fmt.Errorf("GetCleanableOutputs: invalid output data for %s: %w", item.ID, err)
		}
		o, err := ledger.OutputFromBytes(oData)
		if err != nil {
			return nil, fmt.Errorf("GetCleanableOutputs: cannot parse output %s: %w", item.ID, err)
		}
		ret.Outputs = append(ret.Outputs, &ledger.OutputWithID{ID: oid, Output: o})
	}
	return ret, nil
}
