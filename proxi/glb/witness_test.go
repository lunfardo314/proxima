package glb

import (
	"errors"
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// The witness check of kb/api_witnesses.md against scripted witnesses: a
// witness is a reliable branch plus the set of branches it has committed and
// the lineage behind its reliable branch. No HTTP: the check is exercised
// through the branchWitness interface it talks to.

// fakeWitness answers like a node whose committed branches are `known` and whose
// reliable branch is `lrb` with `lineage` behind it (oldest first, including lrb).
type fakeWitness struct {
	down    bool
	lrb     base.TransactionID
	known   map[base.TransactionID]bool
	lineage []base.TransactionID
}

func (w *fakeWitness) GetLatestReliableBranch() (*multistate.BranchDataJSONAble, base.TransactionID, error) {
	if w.down {
		return nil, base.TransactionID{}, errors.New("connection refused")
	}
	return &multistate.BranchDataJSONAble{}, w.lrb, nil
}

func (w *fakeWitness) GetBranchChainTo(toBranch base.TransactionID, fromSlot uint32) ([]base.TransactionID, uint32, error) {
	if !w.known[toBranch] {
		return nil, 0, errors.New("from server: to_branch not known to this source")
	}
	if toBranch != w.lrb {
		return []base.TransactionID{toBranch}, toBranch.Slot(), nil
	}
	ret := make([]base.TransactionID, 0)
	for _, id := range w.lineage {
		if id.Slot() >= fromSlot {
			ret = append(ret, id)
		}
	}
	return ret, toBranch.Slot(), nil
}

func branchAt(slot uint32) base.TransactionID {
	return base.RandomTransactionID(true, 1, base.LedgerTime{Slot: slot})
}

func TestVerifyBranchWithWitnesses(t *testing.T) {
	b10 := branchAt(10)
	b11 := branchAt(11)
	b12 := branchAt(12)
	fork10 := branchAt(10) // a sibling of b10 that lost
	lineage := []base.TransactionID{b10, b11, b12}
	known := func(ids ...base.TransactionID) map[base.TransactionID]bool {
		m := map[base.TransactionID]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	ahead := func() *fakeWitness {
		return &fakeWitness{lrb: b12, known: known(b10, b11, b12, fork10), lineage: lineage}
	}
	run := func(witnesses ...*fakeWitness) error {
		urls := make([]string, len(witnesses))
		byURL := map[string]branchWitness{}
		for i, w := range witnesses {
			urls[i] = string(rune('a' + i))
			byURL[urls[i]] = w
		}
		return verifyBranchWithWitnesses(b10, urls, func(url string) branchWitness { return byURL[url] })
	}

	t.Run("two witnesses ahead agree", func(t *testing.T) {
		require.NoError(t, run(ahead(), ahead()))
	})
	t.Run("witness at the same slot: committed is enough", func(t *testing.T) {
		require.NoError(t, run(&fakeWitness{lrb: fork10, known: known(b10, fork10), lineage: []base.TransactionID{fork10}}))
	})
	t.Run("lagging witness: committed is enough", func(t *testing.T) {
		b9 := branchAt(9)
		require.NoError(t, run(&fakeWitness{lrb: b9, known: known(b9, b10), lineage: []base.TransactionID{b9}}))
	})
	t.Run("witness does not know the branch", func(t *testing.T) {
		err := run(ahead(), &fakeWitness{lrb: b12, known: known(b11, b12), lineage: lineage})
		require.ErrorContains(t, err, "b: has not committed")
	})
	t.Run("committed but off the reliable lineage", func(t *testing.T) {
		err := verifyBranchWithWitnesses(fork10, []string{"a"}, func(string) branchWitness { return ahead() })
		require.ErrorContains(t, err, "not on the lineage")
	})
	t.Run("one down, one agreeing", func(t *testing.T) {
		require.NoError(t, run(&fakeWitness{down: true}, ahead()))
	})
	t.Run("all down", func(t *testing.T) {
		err := run(&fakeWitness{down: true}, &fakeWitness{down: true})
		require.ErrorContains(t, err, "no witness node is reachable")
	})
}

// The witness list is the profile's node_urls without the primary and without
// repeats, trailing slashes ignored.
func TestWitnessURLs(t *testing.T) {
	viper.Set("api.node_url", "http://primary:8000")
	viper.Set("api.node_urls", []string{"http://a:8001/", "http://primary:8000/", "http://a:8001", " ", "http://b:8001"})
	require.Equal(t, []string{"http://a:8001", "http://b:8001"}, WitnessURLs())
	viper.Set("api.node_urls", nil)
	require.Empty(t, WitnessURLs())
}
