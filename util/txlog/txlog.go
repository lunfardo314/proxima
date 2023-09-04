package txlog

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core"
)

// TransactionLog collects simple log records for the particular transaction
type (
	TransactionLog struct {
		mutex     sync.RWMutex
		givenName string
		TxID      *core.TransactionID
		recs      []txLogRecord
	}

	txLogRecord struct {
		ts  time.Time
		msg string
	}
)

const (
	timeLayout = "15:04:05.000000000"
	lineLayout = "%20s   %s"
)

func NewTransactionLog(txid *core.TransactionID, givenName ...string) *TransactionLog {
	ret := &TransactionLog{
		TxID: txid,
		recs: make([]txLogRecord, 0),
	}
	if len(givenName) > 0 {
		ret.givenName = givenName[0]
	}
	return ret
}

func (l *TransactionLog) Logf(format string, args ...any) {
	if l == nil {
		return
	}

	l.mutex.Lock()
	defer l.mutex.Unlock()

	l.recs = append(l.recs, txLogRecord{
		ts:  time.Now(),
		msg: fmt.Sprintf(format, args...),
	})
}

func (l *TransactionLog) WriteLog(w io.Writer) {
	l.mutex.RLock()
	defer l.mutex.RUnlock()

	if l == nil {
		return
	}
	_, _ = fmt.Fprintf(w, "-- Transaction log: name='%s', id=%s\n", l.givenName, l.TxID)

	for i := range l.recs {
		_, _ = fmt.Fprintf(w, lineLayout+"\n", l.recs[i].ts.Format(timeLayout), l.recs[i].msg)
	}
}

func (l *TransactionLog) String() string {
	if l == nil {
		return "(empty)"
	}
	var buf bytes.Buffer
	l.WriteLog(&buf)
	return buf.String()
}
