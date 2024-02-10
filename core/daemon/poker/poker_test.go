package poker

import (
	"context"
	"sync"
	"testing"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

func TestBasic(t *testing.T) {
	const (
		howManyTx    = 10_000
		howManyPokes = 10
	)
	ctx, cancel := context.WithCancel(context.Background())
	log := global.NewDefaultLogging("", zap.DebugLevel, nil)
	p := New(log)

	var wgStop sync.WaitGroup
	wgStop.Add(1)
	p.Start(ctx, &wgStop)

	vids := make([]*vertex.WrappedTx, howManyTx)
	for i := range vids {
		txid := ledger.RandomTransactionID(true, false)
		vids[i] = vertex.WrapTxID(txid)
	}

	counter := new(atomic.Int32)
	var wg sync.WaitGroup

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
	wgStop.Wait()
	require.EqualValues(t, howManyPokes*howManyTx, int(counter.Load()))
}