package txlog

import (
	"testing"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/stretchr/testify/require"
)

func TestTxLog(t *testing.T) {
	t.Run("write log", func(t *testing.T) {
		var txid core.TransactionID
		l := NewTransactionLog(&txid)
		const howMany = 10
		for i := 0; i < howMany; i++ {
			l.Logf("[[%d]]", i)
			time.Sleep(1 * time.Millisecond)
		}
		t.Logf("\n%s", l.String())
	})
	t.Run("nil log", func(t *testing.T) {
		var l *TransactionLog
		require.NotPanics(t, func() {
			l.Logf("[[%d]]", 1)
			l.Logf("[[%d]]", 2)
			l.Logf("[[%d]]", 3)
		})
	})
}
