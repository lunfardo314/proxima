package node

// Optional, read-only memDAG debug API. Disabled unless config key
// `debug.memdag_port` is set. Binds loopback only (reach it over an SSH tunnel);
// it exposes internal node state, so it must not be public. Built for the
// pin/leak investigation — see core/memdag/memdag_debug.go.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lunfardo314/proxima/core/memdag"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/spf13/viper"
)

func (p *ProximaNode) startMemDAGDebugAPIIfEnabled() {
	port := viper.GetInt("debug.memdag_port")
	if port == 0 {
		return
	}
	dag := p.workflow.MemDAG
	mux := http.NewServeMux()

	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
	}
	parseTxID := func(w http.ResponseWriter, r *http.Request) (base.TransactionID, bool) {
		txid, err := base.TransactionIDFromHexString(r.URL.Query().Get("txid"))
		if err != nil {
			http.Error(w, fmt.Sprintf("bad txid: %v", err), http.StatusBadRequest)
			return base.TransactionID{}, false
		}
		return txid, true
	}

	mux.HandleFunc("/debug/memdag/census", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, dag.Census())
	})
	mux.HandleFunc("/debug/memdag/vertices", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, dag.QueryVertices(parseFilter(r)))
	})
	mux.HandleFunc("/debug/memdag/vertex", func(w http.ResponseWriter, r *http.Request) {
		txid, ok := parseTxID(w, r)
		if !ok {
			return
		}
		dump, found := dag.DumpVertex(txid)
		if !found {
			http.Error(w, "not in memDAG", http.StatusNotFound)
			return
		}
		writeJSON(w, dump)
	})
	mux.HandleFunc("/debug/memdag/pinners", func(w http.ResponseWriter, r *http.Request) {
		txid, ok := parseTxID(w, r)
		if !ok {
			return
		}
		writeJSON(w, dag.FindPinners(txid))
	})

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	p.Log().Infof("starting memDAG debug API on '%s' (loopback only)", addr)
	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			p.Log().Warnf("memDAG debug API stopped: %v", err)
		}
	}()
}

func parseFilter(r *http.Request) memdag.VertexFilter {
	q := r.URL.Query()
	f := memdag.VertexFilter{
		Kind:   q.Get("kind"),
		Status: q.Get("status"),
		Sort:   q.Get("sort"),
		Order:  q.Get("order"),
	}
	u32 := func(k string) *uint32 {
		if s := q.Get(k); s != "" {
			if v, err := strconv.ParseUint(s, 10, 32); err == nil {
				x := uint32(v)
				return &x
			}
		}
		return nil
	}
	b := func(k string) *bool {
		if s := q.Get(k); s != "" {
			x := s == "true" || s == "1"
			return &x
		}
		return nil
	}
	f.AddedBefore = u32("added_before")
	f.AddedAfter = u32("added_after")
	f.AddedLagGt = u32("added_lag_gt")
	f.IsBranch = b("is_branch")
	f.IsSequencer = b("is_sequencer")
	f.RefBySeq = b("ref_by_sequencer")
	f.DetachedInMap = b("detached_in_map")
	if s := q.Get("limit"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			f.Limit = v
		}
	}
	return f
}
