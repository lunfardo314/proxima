package poker

import (
	"context"
	"sync"
	"testing"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/utangle/vertex"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func TestBasic(t *testing.T) {
	const (
		howManyTx    = 10_000
		howManyPokes = 10
	)
	ctx, cancel := context.WithCancel(context.Background())
	p := Start(ctx)

	vids := make([]*vertex.WrappedTx, howManyTx)
	var wg sync.WaitGroup
	for i := range vids {
		txid := core.RandomTransactionID(true, false)
		vids[i] = vertex.WrapTxID(txid)
	}

	counter := new(atomic.Int32)

	for i, vid := range vids {
		vid.OnPoke(func(vid1 *vertex.WrappedTx) {
			//t.Logf("poked %s with %s", vid.IDShortString(), vid1.IDShortString())
			counter.Inc()
			wg.Done()
		})
		for j := 0; j < howManyPokes; j++ {
			wg.Add(1)
			idxWaited := (i + j + 1) % howManyTx
			p.PokeMe(vids[i], vids[idxWaited])
		}
	}
	for _, vid := range vids {
		p.PokeAllWith(vid)
	}
	wg.Wait()
	cancel()
	p.WaitStop()
	require.EqualValues(t, howManyPokes*howManyTx, int(counter.Load()))
}
