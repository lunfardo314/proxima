package sequencer

import "github.com/prometheus/client_golang/prometheus"

type sequencerMetrics struct {
	branchCounter       prometheus.Counter
	seqMilestoneCounter prometheus.Counter
}

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
	seq.MetricsRegistry().MustRegister(seq.metrics.branchCounter, seq.metrics.seqMilestoneCounter)
}
