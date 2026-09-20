package node_cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initSeqRatingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "seq_rating",
		Short: "rates the active sequencers as delegation targets, best on top; --tag_along rates them as tag-along targets",
		Long: `Prints the sequencers active in the node's LRB state in the order a 'random'
target is drawn from, best on top, with the rank under each criterion, the
rating (the weighted sum of the ranks, the price criterion counted twice,
smaller is better) and the draw probability.

Delegation criteria: the share left to delegators (descending), the balance
(descending) and the frozen-to-balance ratio (ascending); every sequencer
leaving anything is a candidate, as for the price-taking 'proxi node
consolidate'. 'proxi node delegate' additionally drops those leaving less than
its --cut. Tag-along criteria: the minimum fee (ascending) and the balance
(descending). See kb/sequencer_rating.md.`,
		Args: cobra.NoArgs,
		Run:  runSeqRatingCmd,
	}
	cmd.Flags().Bool("tag_along", false, "rate as tag-along targets instead of delegation targets")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runSeqRatingCmd(cmd *cobra.Command, _ []string) {
	tagAlong, _ := cmd.Flags().GetBool("tag_along")

	active, inactive, err := glb.FetchSequencerCandidates()
	glb.AssertNoError(err)

	criteria := txbuildercore.DelegationCriteria
	candidates := active
	var excluded []txbuildercore.SequencerCandidate
	if tagAlong {
		criteria = txbuildercore.TagAlongCriteria
		glb.Infof("tag-along rating of the %d sequencers active in the last %d slots", len(active), txbuildercore.ActiveSequencerSlots)
	} else {
		candidates = txbuildercore.DelegationCandidates(active, 0)
		for _, c := range active {
			if c.ShareLeft == 0 {
				excluded = append(excluded, c)
			}
		}
		glb.Infof("delegation rating of the %d sequencers active in the last %d slots and leaving delegators anything",
			len(candidates), txbuildercore.ActiveSequencerSlots)
	}
	names := make([]string, len(criteria))
	for i, c := range criteria {
		names[i] = fmt.Sprintf("%s (x%d)", c.Name, c.Weight)
	}
	glb.Infof("ranks are by: %s", strings.Join(names, " / "))
	glb.Infof("balance and frozen coverage in PROX, minimum fee in motes")
	glb.Infof("")

	rated := txbuildercore.RateSequencers(candidates, criteria)
	total := txbuildercore.DrawWeightTotal(len(rated))
	glb.Infof("%3s  %-50s  %-12s  %6s  %-9s  %6s  %5s  %12s  %20s  %20s  %7s",
		"#", "sequencer", "name", "rating", "ranks", "draw", "share", "min fee", "balance", "frozen", "DE")
	for p, r := range rated {
		ranks := make([]string, len(r.Ranks))
		for i, rk := range r.Ranks {
			ranks[i] = strconv.Itoa(rk)
		}
		glb.Infof("%3d  %s  %-12s  %6d  %-9s  %5.1f%%  %5d  %12s  %20s  %20s  %7s",
			p+1, r.ID.String(), r.Name, r.Rating, strings.Join(ranks, "/"), 100*float64(r.Weight)/float64(total),
			r.ShareLeft, util.Th(r.MinimumFee, ","), prox2(r.Balance), prox2(r.FrozenCoverage), deRatio(&r.SequencerCandidate))
	}
	if len(excluded) > 0 {
		glb.Infof("")
		glb.Infof("active but leaving delegators nothing:")
		for _, c := range excluded {
			glb.Infof("     %s  %-12s", c.ID.String(), c.Name)
		}
	}
	if len(inactive) > 0 {
		glb.Infof("")
		glb.Infof("not active (no settled milestone in the last %d slots):", txbuildercore.ActiveSequencerSlots)
		for _, c := range inactive {
			glb.Infof("     %s  %-12s  last milestone in slot %d", c.ID.String(), c.Name, c.Slot)
		}
	}
}

// prox2 renders motes as PROX with two decimals, truncated.
func prox2(motes uint64) string {
	return fmt.Sprintf("%s.%02d", util.Th(motes/base.PROX, ","), motes%base.PROX*100/base.PROX)
}

// deRatio is the frozen-to-balance ratio for display; the rating itself
// compares it exactly, without division.
func deRatio(c *txbuildercore.SequencerCandidate) string {
	if c.Balance == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.3f", float64(c.FrozenCoverage)/float64(c.Balance))
}
