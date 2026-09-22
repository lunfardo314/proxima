package api

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
)

// SequencerCandidate reads off a sequencer output what the sequencer rating
// needs (kb/sequencer_rating.md), so the wallet's 'proxi node seq_rating', the
// chain explorer and the monitor rate the same facts. An output without
// sequencer data leaves delegators everything and asks no fee.
func SequencerCandidate(id base.ChainID, o *ledger.OutputWithID) txbuildercore.SequencerCandidate {
	c := txbuildercore.SequencerCandidate{
		ID:           id,
		Slot:         o.ID.Slot(),
		Balance:      o.Output.TokenBalance(),
		ShareLeft:    1000,
		MinimumTopUp: txbuildercore.MinimumTopUpAmount,
	}
	if frozen := o.Output.FrozenCoverage(0); frozen > 0 {
		c.FrozenCoverage = uint64(frozen)
	}
	if sd, err := ledger.ParseSequencerData(o.Output); err == nil {
		c.Name = sd.Name()
		c.ShareLeft -= sd.InflationProfitMarginPromille()
		c.MinimumFee = sd.MinimumFee()
		c.MinimumTopUp = max(sd.MinimumTopUp(), txbuildercore.MinimumTopUpAmount)
	}
	return c
}

// SequencerRating is a sequencer's place in the delegation rating: the
// weighted rank sum (smaller is better), its 1-based position in the draw
// order and the probability, in percent, that a wallet drawing a random
// delegation target picks it.
type SequencerRating struct {
	Rating      int     `json:"rating"`
	Position    int     `json:"position"`
	Probability float64 `json:"probability"`
}

// DelegationRatings rates the sequencers as delegation targets the way a
// price-taking wallet drawing a random target does: only those active within
// txbuildercore.ActiveSequencerSlots of the LRB and leaving delegators
// anything take part. Keyed by chain ID; a sequencer not rated is absent.
func DelegationRatings(candidates []txbuildercore.SequencerCandidate, lrbSlot uint32) map[base.ChainID]SequencerRating {
	active := make([]txbuildercore.SequencerCandidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Active(lrbSlot) {
			active = append(active, c)
		}
	}
	rated := txbuildercore.RateSequencers(txbuildercore.DelegationCandidates(active, 0), txbuildercore.DelegationCriteria)
	total := txbuildercore.DrawWeightTotal(len(rated))
	ret := make(map[base.ChainID]SequencerRating, len(rated))
	for p, r := range rated {
		ret[r.ID] = SequencerRating{
			Rating:      r.Rating,
			Position:    p + 1,
			Probability: 100 * float64(r.Weight) / float64(total),
		}
	}
	return ret
}
