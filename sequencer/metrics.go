package sequencer

import "github.com/prometheus/client_golang/prometheus"

type sequencerMetrics struct {
	branchCounter       *prometheus.Counter
	seqMilestoneCounter *prometheus.Counter
}
