package sequencer

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/prometheus/client_golang/prometheus"
)

type sequencerMetrics struct {
	branchCounter       prometheus.Counter
	seqMilestoneCounter prometheus.Counter
	targets             prometheus.Counter
	backlogSize         prometheus.Gauge
	ownMilestones       prometheus.Gauge
	// endorsement distribution: counter per endorsement count (0, 1, 2, ... maxEndorsements)
	endorsementCounters []prometheus.Counter
}

const maxEndorsementMetricLabel = 8 // matches max endorsements in ledger

func (seq *Sequencer) registerMetrics() {
	seq.Assertf(seq.config.SingleSequencerEnforced, "seq.config.SingleSequencerEnforced")

	seq.metrics.seqMilestoneCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_seq_milestones",
		Help: "sequencer transaction submitted (including branches)",
	})
	seq.metrics.branchCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_seq_branches",
		Help: "branches submitted",
	})
	seq.metrics.targets = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_seq_targets",
		Help: "number of sequencer targets",
	})

	seq.MetricsRegistry().MustRegister(
		seq.metrics.branchCounter,
		seq.metrics.seqMilestoneCounter,
		seq.metrics.targets,
	)

	// endorsement count distribution: one counter per endorsement count
	seq.metrics.endorsementCounters = make([]prometheus.Counter, maxEndorsementMetricLabel+1)
	for i := 0; i <= maxEndorsementMetricLabel; i++ {
		seq.metrics.endorsementCounters[i] = prometheus.NewCounter(prometheus.CounterOpts{
			Name: fmt.Sprintf("proxima_seq_endorsements_%d", i),
			Help: fmt.Sprintf("number of sequencer transactions with %d endorsements", i),
		})
		seq.MetricsRegistry().MustRegister(seq.metrics.endorsementCounters[i])
	}

	seq.metrics.backlogSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_seq_backlog_size",
		Help: "number of outputs in the own sequencer's backlog",
	})
	seq.metrics.ownMilestones = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_seq_own_milestones",
		Help: "number of own milestones",
	})
	seq.MetricsRegistry().MustRegister(
		seq.metrics.backlogSize,
		seq.metrics.ownMilestones,
	)
}

func (seq *Sequencer) onMilestoneSubmittedMetrics(vid *vertex.WrappedTx) {
	if seq.metrics == nil {
		return
	}
	seq.metrics.seqMilestoneCounter.Inc()
	if vid.IsBranchTransaction() {
		seq.metrics.branchCounter.Inc()
	}
}

func (seq *Sequencer) newTargetSet() {
	if seq.metrics == nil {
		return
	}
	seq.metrics.targets.Inc()
}

func (seq *Sequencer) EvidenceEndorsementCount(numEndorsements int) {
	if seq.metrics == nil {
		return
	}
	idx := numEndorsements
	if idx > maxEndorsementMetricLabel {
		idx = maxEndorsementMetricLabel
	}
	seq.metrics.endorsementCounters[idx].Inc()
}

func (seq *Sequencer) EvidenceBacklogSize(size int) {
	if seq.metrics == nil {
		return
	}
	seq.metrics.backlogSize.Set(float64(size))
}
