package txstore

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

type testMetrics struct{ reg *prometheus.Registry }

func (m testMetrics) MetricsRegistry() *prometheus.Registry { return m.reg }

// The node persists transactions only through PersistTxBytesBatch, so the
// store's counters must move on that path, not only on PersistTxBytes.
func TestBatchWriteUpdatesMetrics(t *testing.T) {
	s := NewSimpleTxBytesStore(common.NewInMemoryKVStore(), testMetrics{prometheus.NewRegistry()})

	batch := map[base.TransactionID][]byte{
		{1}: make([]byte, 100),
		{2}: make([]byte, 250),
	}
	require.NoError(t, s.PersistTxBytesBatch(batch))

	require.EqualValues(t, 2, read(t, s.txCounter).GetCounter().GetValue())
	require.EqualValues(t, 350, read(t, s.txBytesCounter).GetCounter().GetValue())
	require.EqualValues(t, 2, read(t, s.txBytesSizeHistogram).GetHistogram().GetSampleCount())
}

func read(t *testing.T, m prometheus.Metric) *dto.Metric {
	var ret dto.Metric
	require.NoError(t, m.Write(&ret))
	return &ret
}
