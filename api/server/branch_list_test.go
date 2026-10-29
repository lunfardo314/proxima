package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
	"github.com/stretchr/testify/require"
)

// countingStore counts reads, so a test can tell whether a request touched the state at all.
type countingStore struct {
	global.Store
	reads int
}

func (s *countingStore) Get(key []byte) []byte {
	s.reads++
	return s.Store.Get(key)
}

type branchListEnv struct {
	environment
	store *countingStore
}

func (e *branchListEnv) StateStore() global.Store { return e.store }

// TestBranchListRangeBound pins the bound on get_branch_list. The response keeps the oldest
// branches above from_slot, so the source has to walk all the way back to it; a range longer than
// maxBranchListSlots is refused before the state is read at all, so a node stuck far behind and
// polling for its old position costs nothing. A range within the bound passes the check and gets
// the usual answer, here that the source does not know the branch.
func TestBranchListRangeBound(t *testing.T) {
	env := &branchListEnv{store: &countingStore{Store: common.NewInMemoryKVStore()}}
	srv := &server{environment: env}
	request := func(toSlot, fromSlot uint32) string {
		toBranch := base.RandomTransactionID(true, 0, base.LedgerTime{Slot: toSlot})
		rec := httptest.NewRecorder()
		srv.getBranchList(rec, httptest.NewRequest("GET",
			fmt.Sprintf("%s?to_branch=%s&from_slot=%d", api.PathGetBranchList, toBranch.StringHex(), fromSlot), nil))
		var resp api.BranchList
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return resp.Error.Error
	}

	const fromSlot = 1000
	require.Contains(t, request(fromSlot+maxBranchListSlots+1, fromSlot), "range too long")
	require.Zero(t, env.store.reads, "a request beyond the bound read the state")

	require.Contains(t, request(fromSlot+maxBranchListSlots, fromSlot), "not known to this source")
	require.Positive(t, env.store.reads, "a request within the bound did not reach the state")
}
