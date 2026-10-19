package mine

import (
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type pendingTestTransit struct {
	tip     *mineTip
	txBytes []byte
}

// Use genuine signed, solved transits so these tests exercise pending replay
// through the same verifier as a live stream, rather than inserting tree nodes.
func pendingTestChain(t *testing.T, f *verifyFixture, count int) []pendingTestTransit {
	t.Helper()
	ret := make([]pendingTestTransit, 0, count)
	pred := f.pred
	for range count {
		txBytes := (&verifyFixture{m: f.m, pred: pred}).mineOne(t)
		tip, err := verifyMineTransit(f.m.lib, f.m.consts, pred, txBytes)
		require.NoError(t, err)
		ret = append(ret, pendingTestTransit{tip: tip, txBytes: txBytes})
		pred = tip
	}
	return ret
}

func TestPendingTransitConfirmedParent(t *testing.T) {
	f := newVerifyFixture(t, 6)
	f.m.tree = newMineTree(f.pred)
	chain := pendingTestChain(t, f, 2)
	parent, child := chain[0], chain[1]

	// A live-only stream can miss a parent that existed before subscription.
	// Its child must become usable when the node later confirms that parent.
	f.m.onStreamedTransit(child.txBytes)
	require.Nil(t, f.m.tree.tipFor(child.tip.oid))
	require.Equal(t, f.pred.oid, f.m.tree.bestTip().oid)

	require.Equal(t, tipReanchor, f.m.onConfirmedTip(parent.tip))
	require.NotNil(t, f.m.tree.tipFor(child.tip.oid))
	require.Equal(t, child.tip.oid, f.m.tree.bestTip().oid)
}

func TestPendingTransitConfirmedParentReplaysDescendants(t *testing.T) {
	f := newVerifyFixture(t, 6)
	f.m.tree = newMineTree(f.pred)
	chain := pendingTestChain(t, f, 4)

	// Descendants may arrive in any order. Advancing the confirmed root must
	// retain the queued chain long enough to verify it from parent to child.
	for i := len(chain) - 1; i > 0; i-- {
		f.m.onStreamedTransit(chain[i].txBytes)
	}
	require.Equal(t, f.pred.oid, f.m.tree.bestTip().oid)
	f.m.onConfirmedTip(chain[0].tip)

	for _, transit := range chain[1:] {
		require.NotNil(t, f.m.tree.tipFor(transit.tip.oid))
	}
	require.Equal(t, chain[len(chain)-1].tip.oid, f.m.tree.bestTip().oid)
}

func TestPendingTransitConfirmedParentRejectsWeakProof(t *testing.T) {
	f := newVerifyFixture(t, 6)
	f.m.tree = newMineTree(f.pred)
	chain := pendingTestChain(t, f, 2)
	parent, child := chain[0], chain[1]
	weak := (&verifyFixture{m: f.m, pred: parent.tip}).mineUnsolved(t)
	_, err := verifyMineTransit(f.m.lib, f.m.consts, parent.tip, weak)
	require.Error(t, err)

	// Being queued does not make a transit trusted. An invalid sibling must
	// neither enter the tree nor prevent the following valid child replay.
	f.m.onStreamedTransit(weak)
	f.m.onStreamedTransit(child.txBytes)
	f.m.onConfirmedTip(parent.tip)

	require.Equal(t, child.tip.oid, f.m.tree.bestTip().oid)
	f.m.tree.mu.Lock()
	defer f.m.tree.mu.Unlock()
	require.Len(t, f.m.tree.nodes, 1, "only the verified child may enter the tree")
}

func TestPendingTransitStreamedParentReplaysDescendants(t *testing.T) {
	f := newVerifyFixture(t, 6)
	f.m.tree = newMineTree(f.pred)
	chain := pendingTestChain(t, f, 3)

	// Preserve the existing replay path when the missing parent arrives on
	// the stream instead of through a confirmation poll.
	for i := len(chain) - 1; i >= 0; i-- {
		f.m.onStreamedTransit(chain[i].txBytes)
	}
	require.Equal(t, chain[len(chain)-1].tip.oid, f.m.tree.bestTip().oid)
}

func TestPendingTransitConcurrentConfirmation(t *testing.T) {
	f := newVerifyFixture(t, 6)
	chain := pendingTestChain(t, f, 2)
	parent, child := chain[0], chain[1]

	// Verification fixtures are prepared before launching goroutines. Vary
	// which callback gets a head start without timing-dependent sleeps; both
	// callbacks must leave the child available regardless of their ordering.
	for i := range 64 {
		m := &miner{lib: f.m.lib, consts: f.m.consts, tree: newMineTree(f.pred)}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for range i % 4 {
				runtime.Gosched()
			}
			m.onStreamedTransit(child.txBytes)
		}()
		go func() {
			defer wg.Done()
			<-start
			for range (i / 4) % 4 {
				runtime.Gosched()
			}
			m.onConfirmedTip(parent.tip)
		}()
		close(start)
		wg.Wait()
		require.Equalf(t, child.tip.oid, m.tree.bestTip().oid, "schedule %d", i)
	}
}
