package backlog

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
)

// One reason can refuse a stream of outputs, e.g. a miner that tops up a delegation which no
// longer exists with every reward. The first rejectionsInFull outputs of a reason are logged in
// full, later ones summed up in one line at most once per rejectionSummaryPeriod. A reason not
// seen for rejectionForgetAfter is forgotten, so it is logged in full again when it comes back.
const (
	rejectionsInFull       = 3
	rejectionSummaryPeriod = time.Minute
	rejectionForgetAfter   = time.Hour
)

type rejectionRecord struct {
	loggedInFull int
	// repeats since the last summary, and the latest of them
	pending     int
	latest      string
	lastSummary time.Time
	lastSeen    time.Time
}

// note records one more rejection at now. It says whether to log this one in full, or else how
// many repeats to sum up now (0: not yet) and the latest of them.
func (r *rejectionRecord) note(oid string, now time.Time) (inFull bool, summarize int, latest string) {
	r.lastSeen = now
	if r.loggedInFull < rejectionsInFull {
		r.loggedInFull++
		return true, 0, ""
	}
	r.pending++
	r.latest = oid
	if now.Sub(r.lastSummary) < rejectionSummaryPeriod {
		return false, 0, ""
	}
	summarize, latest = r.pending, r.latest
	r.pending, r.lastSummary = 0, now
	return false, summarize, latest
}

// ReportPermanentRejection logs an output the sequencer refuses for good. details renders the
// output, only when it is logged in full.
func (b *TagAlongBacklog) ReportPermanentRejection(oid base.OutputID, reason error, details func() string) {
	key := fmt.Sprint(reason)

	b.mutex.Lock()
	r := b.rejections[key]
	if r == nil {
		r = &rejectionRecord{}
		b.rejections[key] = r
	}
	inFull, n, latest := r.note(oid.StringShort(), time.Now())
	b.mutex.Unlock()

	switch {
	case inFull:
		b.WarnTopicf("tag_along", 0, "TAG_ALONG: output cannot be consumed PERMANENTLY, reason = '%s'\n%s", key, details)
	case n > 0:
		b.logRejectionSummary(key, n, latest)
	}
}

func (b *TagAlongBacklog) logRejectionSummary(reason string, n int, latest string) {
	b.WarnTopicf("tag_along", 0, "TAG_ALONG: %d more output(s) cannot be consumed PERMANENTLY for the same reason, latest %s, reason = '%s'",
		n, latest, reason)
}

// flushRejections sums up the repeats a quiet reason left unreported, and forgets the reasons
// not seen for rejectionForgetAfter.
func (b *TagAlongBacklog) flushRejections() {
	type summary struct {
		reason, latest string
		n              int
	}
	var due []summary
	now := time.Now()

	b.mutex.Lock()
	for key, r := range b.rejections {
		if r.pending > 0 && now.Sub(r.lastSummary) >= rejectionSummaryPeriod {
			due = append(due, summary{reason: key, latest: r.latest, n: r.pending})
			r.pending, r.lastSummary = 0, now
		}
		if r.pending == 0 && now.Sub(r.lastSeen) >= rejectionForgetAfter {
			delete(b.rejections, key)
		}
	}
	b.mutex.Unlock()

	for _, s := range due {
		b.logRejectionSummary(s.reason, s.n, s.latest)
	}
}
