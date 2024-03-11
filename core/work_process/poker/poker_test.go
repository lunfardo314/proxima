package poker

import (
	"sync"
	"testing"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

func init() {
	ledger.InitWithTestingLedgerIDData()
}

func TestBasic(t *testing.T) {
	const (
		howManyTx    = 10_000
		howManyPokes = 10
	)
	glb := global.NewDefault()
	p := New(glb)

	p.Start()

	vids := make([]*vertex.WrappedTx, howManyTx)
	for i := range vids {
		txid := ledger.RandomTransactionID(true)
		vids[i] = vertex.WrapTxID(txid)
	}

	counter := new(atomic.Int32)
	var wg sync.WaitGroup

	for i, vid := range vids {
		vid.OnPoke(func() {
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
	glb.Stop()
	glb.MustWaitAllWorkProcessesStop()
	require.EqualValues(t, howManyPokes*howManyTx, int(counter.Load()))
}
