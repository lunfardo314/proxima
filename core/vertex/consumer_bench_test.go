package vertex

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
)

// benchCone builds a past cone of n producers, each with two outputs consumed by members of the cone and
// one more consumer outside it, the shape the coverage, mutation and cleanup passes read. It compiles
// against strong and weak consumer edges alike, so the same benchmark compares the two builds.
func benchCone(n int) (*PastCone, []*WrappedTx) {
	baselineID := base.RandomTransactionID(true, 2, base.T(999, 0))
	pc := &PastCone{PastConeBase: NewPastConeBase(&baselineID)}
	producers := make([]*WrappedTx, n)
	for i := range producers {
		p := WrapTxID(base.RandomTransactionID(false, 2, base.T(1000, 10)))
		pc.vertices[p] = FlagPastConeVertexKnown
		for idx := byte(0); idx < 2; idx++ {
			c := WrapTxID(base.RandomTransactionID(false, 1, base.T(1001, 10)))
			pc.vertices[c] = FlagPastConeVertexKnown
			p.AddConsumer(idx, c)
		}
		// an outside consumer, kept alive by the slice below like a vertex held elsewhere in the memDAG
		outside := WrapTxID(base.RandomTransactionID(false, 1, base.T(1001, 20)))
		p.AddConsumer(0, outside)
		producers[i] = p
		benchOutside = append(benchOutside, outside)
	}
	return pc, producers
}

var benchOutside []*WrappedTx

// BenchmarkConsumersByOutputIndex measures the consumer read every past-cone pass makes per vertex.
func BenchmarkConsumersByOutputIndex(b *testing.B) {
	pc, producers := benchCone(1000)
	b.ReportAllocs()
	b.ResetTimer()
	n := 0
	for i := 0; i < b.N; i++ {
		n += len(pc.consumersByOutputIndex(producers[i%len(producers)]))
	}
	if n == 0 {
		b.Fatal("no consumers found")
	}
}

// BenchmarkAddConsumer measures registering a consumer, done once per input on attachment: the usual
// shape, one or two consumers per output.
func BenchmarkAddConsumer(b *testing.B) {
	consumers := make([]*WrappedTx, 2)
	for i := range consumers {
		consumers[i] = WrapTxID(base.RandomTransactionID(false, 1, base.T(1001, 10)))
	}
	producers := make([]*WrappedTx, 4096)
	for i := range producers {
		producers[i] = WrapTxID(base.RandomTransactionID(false, 1, base.T(1000, 10)))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := producers[i%len(producers)]
		if i%len(producers) == 0 && i > 0 {
			b.StopTimer()
			for _, p := range producers {
				p.consumed = nil
			}
			b.StartTimer()
		}
		p.AddConsumer(0, consumers[(i/len(producers))%2])
	}
}

// BenchmarkAddConsumerManyDoubleSpends measures registering consumers on one output that already has
// hundreds, as conflict spam on one output produces: it must stay O(1) per insert on average.
func BenchmarkAddConsumerManyDoubleSpends(b *testing.B) {
	producer := WrapTxID(base.RandomTransactionID(false, 2, base.T(1000, 10)))
	consumers := make([]*WrappedTx, 1024)
	for i := range consumers {
		consumers[i] = WrapTxID(base.RandomTransactionID(false, 1, base.T(1001, 10)))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		producer.AddConsumer(byte(i%2), consumers[i%len(consumers)])
	}
}
