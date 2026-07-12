package vertex

import (
	"sync/atomic"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/lines"
)

// Consumer-edge generation instrument for the branch mutation-set non-conservation investigation.
//
// The suspected mechanism is a non-atomic read: the branch attacher builds its mutation set by
// walking the past cone's live `consumed` maps across several passes (CheckAndClean, SlotInflation,
// Mutations) with no snapshot, while other attachers concurrently first-register consumer edges
// (AddConsumer) on the same producers. An edge that lands between two passes makes the conservation
// sum transiently unbalanced.
//
// The earlier diagnostic wove three full-cone read passes into CheckAndClean; each took every cone
// member's `mutexDescendants` lock — the SAME locks AddConsumer writes under — which drained the
// pending writer and closed the race window, so the bug stopped reproducing. This instrument avoids
// that: the only always-on cost is a single lock-free atomic increment per FIRST-TIME edge, on a
// counter unrelated to `mutexDescendants`, so it establishes no happens-before between an edge
// insert and the branch reader and cannot mask the race. All forensic work (ring dump, pointer
// comparison) runs only on the failure path, which never executes in the healthy case.
//
// The branch attacher samples ConsumerEdgeGen() right after CheckAndClean and again after
// Mutations(). If the conservation guard fires and the counter advanced, the edges recorded in that
// generation window (filtered to this cone by PastCone.ConsumerEdgesInWindow) name the concrete
// edges that landed mid-build and, via the consumer txid, the walker that registered them.

const consumerEdgeRingSize = 8192

type consumerEdgeRecord struct {
	gen      uint64
	producer base.TransactionID
	idx      byte
	consumer base.TransactionID
}

var (
	// consumerEdgeGen is a process-wide monotone counter of first-time consumer edges. Atomic,
	// lock-free: the single hot-path cost of the instrument.
	consumerEdgeGen atomic.Uint64
	// consumerEdgeRing is a fixed-size best-effort log of recent first-time edges, indexed by
	// gen % size. Writes are lock-free (plain stores) — a torn entry is possible only on ring
	// wrap-around under extreme concurrency and is acceptable for a failure-path-only forensic.
	consumerEdgeRing [consumerEdgeRingSize]consumerEdgeRecord
)

// recordConsumerEdge stamps one first-time edge with the next generation and logs it. Called from
// AddConsumer. Hot-path cost: one atomic add plus a handful of plain stores; no lock.
func recordConsumerEdge(producer base.TransactionID, idx byte, consumer base.TransactionID) {
	gen := consumerEdgeGen.Add(1)
	consumerEdgeRing[gen%consumerEdgeRingSize] = consumerEdgeRecord{
		gen: gen, producer: producer, idx: idx, consumer: consumer,
	}
}

// ConsumerEdgeGen returns the current first-time-consumer-edge generation. The branch attacher
// samples it before and after building the mutation set to detect concurrent edge inserts.
func ConsumerEdgeGen() uint64 {
	return consumerEdgeGen.Load()
}

// dumpConsumerEdgeRing returns the edges recorded with generation in (fromGen, toGen], oldest first.
// `relevant`, if non-nil, restricts output to edges whose producer or consumer satisfies it (used to
// filter the global stream down to a single past cone). Failure-path only.
func dumpConsumerEdgeRing(fromGen, toGen uint64, relevant func(base.TransactionID) bool) *lines.Lines {
	ret := lines.New()
	now := consumerEdgeGen.Load()
	if toGen > now {
		toGen = now
	}
	// oldest generation still retained in the ring
	oldest := uint64(1)
	if now > consumerEdgeRingSize {
		oldest = now - consumerEdgeRingSize + 1
	}
	if fromGen+1 < oldest {
		ret.Add("(warning: %d edge(s) in the window were overwritten in the ring before the dump)", oldest-(fromGen+1))
		fromGen = oldest - 1
	}
	n := 0
	for g := fromGen + 1; g <= toGen; g++ {
		r := consumerEdgeRing[g%consumerEdgeRingSize]
		if r.gen != g {
			continue // slot already reused by a newer generation
		}
		if relevant != nil && !relevant(r.producer) && !relevant(r.consumer) {
			continue
		}
		ret.Add("gen %d: %s#%d -> %s", r.gen, r.producer.StringShort(), r.idx, r.consumer.StringShort())
		n++
	}
	ret.Add("---- %d edge(s) in gen window (%d, %d] %s ----", n, fromGen, toGen,
		map[bool]string{true: "relevant to this cone", false: "total"}[relevant != nil])
	return ret
}
