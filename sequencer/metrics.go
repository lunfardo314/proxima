package sequencer

import (
	"fmt"

	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/sequencer/task"
	"github.com/prometheus/client_golang/prometheus"
)

type sequencerMetrics struct {
	branchCounter           prometheus.Counter
	seqMilestoneCounter     prometheus.Counter
	targets                 prometheus.Counter
	proposalsByStrategy     map[string]prometheus.Counter
	bestProposalsByStrategy map[string]prometheus.Counter
	backlogSize             prometheus.Gauge
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
	seq.metrics.targets = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "proxima_seq_targets",
		Help: "number of sequencer targets",
	})

	seq.MetricsRegistry().MustRegister(
		seq.metrics.branchCounter,
		seq.metrics.seqMilestoneCounter,
		seq.metrics.targets,
	)

	// all proposal counters per strategy
	seq.metrics.proposalsByStrategy = make(map[string]prometheus.Counter)
	for _, s := range task.AllProposingStrategies {
		seq.metrics.proposalsByStrategy[s.ShortName] = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "proxima_seq_proposals_" + s.ShortName,
			Help: fmt.Sprintf("number of proposals submitted by proposer %s(%s)", s.Name, s.ShortName),
		})
	}
	for _, m := range seq.metrics.proposalsByStrategy {
		seq.MetricsRegistry().MustRegister(m)
	}

	// best proposal counters per strategy
	seq.metrics.bestProposalsByStrategy = make(map[string]prometheus.Counter)
	for _, s := range task.AllProposingStrategies {
		seq.metrics.bestProposalsByStrategy[s.ShortName] = prometheus.NewCounter(prometheus.CounterOpts{
			Name: "proxima_seq_best_proposals_" + s.ShortName,
			Help: fmt.Sprintf("number of best proposals for the target %s(%s)", s.Name, s.ShortName),
		})
	}
	for _, m := range seq.metrics.bestProposalsByStrategy {
		seq.MetricsRegistry().MustRegister(m)
	}

	seq.metrics.backlogSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "proxima_seq_backlog_size",
		Help: "number of outputs in the own sequencer's backlog",
	})
	seq.MetricsRegistry().MustRegister(seq.metrics.backlogSize)

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

func (seq *Sequencer) EvidenceProposal(strategyShortName string) {
	if seq.metrics == nil {
		return
	}
	seq.metrics.proposalsByStrategy[strategyShortName].Inc()
}

func (seq *Sequencer) EvidenceBestProposalForTheTarget(strategyShortName string) {
	if seq.metrics == nil {
		return
	}
	seq.metrics.bestProposalsByStrategy[strategyShortName].Inc()
}

func (seq *Sequencer) EvidenceBacklogSize(size int) {
	if seq.metrics == nil {
		return
	}
	seq.metrics.backlogSize.Set(float64(size))
}
