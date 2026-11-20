package vertex

import (
	"runtime"
	"testing"
	"weak"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// makeConsumerChain makes a chain of n consumers after the producer, each consuming output 0 of
// the one before, and returns weak handles to them. Nothing outside the chain references them.
func makeConsumerChain(producer *WrappedTx, n int) []weak.Pointer[WrappedTx] {
	ret := make([]weak.Pointer[WrappedTx], n)
	prev := producer
	for i := range ret {
		c := WrapTxID(base.RandomTransactionID(false, 1, base.T(1001+uint32(i), 10)))
		prev.AddConsumer(0, c)
		ret[i] = weak.Make(c)
		prev = c
	}
	return ret
}

// TestHeldProducerDoesNotPinConsumers: a vertex held alive (here by the test, in the network by a young
// vertex consuming an old output, such as a stuck sequencer's bootstrap transaction every slot) no longer
// keeps its consumers alive, nor theirs: the whole forward closure behind it is reclaimed. With strong
// consumer edges every one of them survived as long as the producer did.
func TestHeldProducerDoesNotPinConsumers(t *testing.T) {
	producer := WrapTxID(base.RandomTransactionID(false, 1, base.T(1000, 10)))
	handles := makeConsumerChain(producer, 1000)

	runtime.GC()
	runtime.GC()
	for i, h := range handles {
		require.Nil(t, h.Value(), "consumer %d of the chain is still alive", i)
	}
	require.Empty(t, producer.ConsumersOf(0), "a reclaimed consumer is not reported")
	nOutputs, nConflicts := producer.NumConsumers()
	require.Zero(t, nOutputs)
	require.Zero(t, nConflicts)

	// a consumer added later is reported alone: the reclaimed one's entry is skipped
	c := WrapTxID(base.RandomTransactionID(false, 1, base.T(1005, 10)))
	producer.AddConsumer(0, c)
	require.EqualValues(t, 1, len(producer.ConsumersOf(0)))
	require.True(t, producer.ConsumersOf(0).Contains(c))
	runtime.KeepAlive(producer)
}

// TestPastConeKeepsItsConsumersVisible: a consumer that is a member of a past cone is held by the cone, so
// it stays in its producer's consumer set however long the cone lives. Every past-cone computation reads
// consumers only through the cone, so it sees what it saw with strong consumer edges.
func TestPastConeKeepsItsConsumersVisible(t *testing.T) {
	baselineID := base.RandomTransactionID(true, 2, base.T(999, 0))
	producer := WrapTxID(base.RandomTransactionID(false, 1, base.T(1000, 10)))
	pc := &PastCone{PastConeBase: NewPastConeBase(&baselineID)}
	pc.vertices[producer] = FlagPastConeVertexKnown

	// the member consumer and a non-member consumer of the same output: a double spend the cone does not see
	func() {
		member := WrapTxID(base.RandomTransactionID(false, 1, base.T(1001, 10)))
		pc.vertices[member] = FlagPastConeVertexKnown
		producer.AddConsumer(0, member)
		producer.AddConsumer(0, WrapTxID(base.RandomTransactionID(false, 1, base.T(1001, 20))))
	}()
	runtime.GC()
	runtime.GC()

	byIdx := pc.consumersByOutputIndex(producer)
	require.EqualValues(t, 1, len(byIdx[0]), "the cone's own consumer must stay visible")
	require.True(t, pc.IsKnown(byIdx[0][0]))
	require.EqualValues(t, 1, len(producer.ConsumersOf(0)), "the non-member consumer, referenced by nothing, is reclaimed")
}
