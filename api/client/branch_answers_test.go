package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// serveJSON answers every request with v marshalled to JSON.
func serveJSON(t *testing.T, v any) *APIClient {
	data, err := json.Marshal(v)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return NewWithGoogleDNS(srv.URL)
}

// TestBranchAnswersChecked checks that the client refuses a node's answer that forward sync could not
// use as a branch lineage: a latest reliable branch or a chain entry without the branch flag, and a
// chain out of slot order or outside [fromSlot, toBranch.Slot()]. Forward sync takes these IDs from
// other nodes and commits them as branches. A well-formed chain, the boundary branch at fromSlot
// included, is accepted.
func TestBranchAnswersChecked(t *testing.T) {
	branch := func(slot uint32) base.TransactionID { return base.RandomTransactionID(true, 1, base.T(slot, 0)) }
	nonBranch := base.RandomTransactionID(true, 1, base.T(105, 3))

	_, _, err := serveJSON(t, &api.LatestReliableBranch{BranchID: nonBranch}).GetLatestReliableBranch()
	require.Error(t, err)
	_, id, err := serveJSON(t, &api.LatestReliableBranch{BranchID: branch(105)}).GetLatestReliableBranch()
	require.NoError(t, err)
	require.True(t, id.IsBranchTransaction())

	to := branch(110)
	chainOf := func(ids ...base.TransactionID) api.BranchList {
		ret := api.BranchList{TopSlot: to.Slot()}
		for _, id := range ids {
			ret.Branches = append(ret.Branches, id.StringHex())
		}
		return ret
	}
	for name, chain := range map[string]api.BranchList{
		"non-branch entry":     chainOf(branch(101), nonBranch, to),
		"out of order":         chainOf(branch(103), branch(102), to),
		"repeated slot":        chainOf(branch(102), branch(102), to),
		"below fromSlot":       chainOf(branch(99), branch(102), to),
		"past toBranch's slot": chainOf(branch(102), branch(111)),
	} {
		_, _, err = serveJSON(t, chain).GetBranchChainTo(to, 100)
		require.Error(t, err, name)
	}
	got, _, err := serveJSON(t, chainOf(branch(100), branch(104), to)).GetBranchChainTo(to, 100)
	require.NoError(t, err)
	require.Len(t, got, 3)
}
