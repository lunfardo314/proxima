// Package holders serves the holders browser: the capital of the latest
// reliable branch totalled per holder ID, as the /holders page and as the
// get_holders JSON endpoint behind it. Both walk the whole UTXO set of the
// LRB, so they are off by default and switched on by node configuration,
// which also sets how many UTXOs one scan may walk.
package holders

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/logo"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

//go:embed holders.html
var holdersHTML []byte

// holdersPage carries the horizontal lockup in the header and the bare mark
// as the tab icon. Light variants: the page is drawn on the monitor's palette.
var holdersPage = logo.Page(holdersHTML, logo.LockupOnLight, logo.MarkOnLight)

const (
	// ConfigKeyEnable switches the page and the endpoint on. Off by default:
	// one request costs a scan of the whole state.
	ConfigKeyEnable = "api.get_holders.enable"
	// ConfigKeyMaxUTXOs is how many UTXOs one scan walks at most. A request
	// may lower it with 'max_utxos', never raise it.
	ConfigKeyMaxUTXOs = "api.get_holders.max_utxos"
	// DefaultMaxUTXOs applies when the key is absent or not positive.
	DefaultMaxUTXOs = 100_000
	// MaxUTXOsCeiling is the hard cap the configured value is clamped to, so
	// that a grown state cannot make one request arbitrarily expensive.
	MaxUTXOsCeiling = 1_000_000
)

// Env is what the holders browser needs from the node API server.
type Env interface {
	LatestReliableState() (multistate.SugaredStateReader, error)
}

// Register wires the page and the JSON endpoint into the supplied addHandler.
// Both answer with a clear refusal while the browser is disabled, so a wallet
// pointed at such a node learns why instead of getting a 404.
func Register(addHandler func(string, func(http.ResponseWriter, *http.Request)), env Env) {
	addHandler(api.PathHolders, servePage)
	addHandler(api.PathGetHolders, func(w http.ResponseWriter, r *http.Request) { serveData(w, r, env) })
}

func enabled() bool {
	return viper.GetBool(ConfigKeyEnable)
}

// maxUTXOs is the configured scan cap, defaulted and clamped to the ceiling
func maxUTXOs() int {
	n := viper.GetInt(ConfigKeyMaxUTXOs)
	if n <= 0 {
		return DefaultMaxUTXOs
	}
	return min(n, MaxUTXOsCeiling)
}

func servePage(w http.ResponseWriter, _ *http.Request) {
	if !enabled() {
		http.Error(w, "holders browser is disabled by node configuration ("+ConfigKeyEnable+")", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(holdersPage)
}

// serveData totals the capital per holder: all of it, and the idle part which
// is neither working in a sequencer chain nor delegated to one.
// GET request format: '/api/v1/get_holders?[max_utxos=<n>]'
func serveData(w http.ResponseWriter, r *http.Request, env Env) {
	api.SetHeader(w)

	if !enabled() {
		api.WriteErr(w, "get_holders is disabled by node configuration ("+ConfigKeyEnable+")")
		return
	}

	limit := maxUTXOs()
	if lst, ok := r.URL.Query()["max_utxos"]; ok {
		n, err := strconv.Atoi(lst[0])
		if err != nil || n <= 0 {
			api.WriteErr(w, "max_utxos: positive integer expected")
			return
		}
		limit = min(n, limit)
	}

	resp := api.Holders{
		Holders: make(map[string]api.HolderTotals),
	}
	err := util.CatchPanicOrError(func() error {
		rdr, err := env.LatestReliableState()
		if err != nil {
			return err
		}
		stem := rdr.GetStemOutput()
		lrbid := stem.ID.TransactionID()
		resp.LRBID = lrbid.StringHex()
		stemLock, ok := stem.Output.StemLock()
		util.Assertf(ok, "get_holders: stem lock expected")
		resp.Supply = stemLock.TotalSupply

		h := rdr.Holdings(limit)
		resp.NumScanned = h.NumScanned
		resp.Truncated = h.Truncated
		resp.Other = api.HolderTotals(h.Other)
		for holder, hi := range h.Holders {
			resp.Holders[hex.EncodeToString(holder[:])] = api.HolderTotals(hi)
		}
		return nil
	})
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	respBin, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	if _, err = w.Write(respBin); err != nil {
		// a slow or vanished reader is the client's condition, not ours
		return
	}
}
