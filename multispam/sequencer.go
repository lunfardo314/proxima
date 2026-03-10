package multispam

import (
	"math/rand"
	"sync"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
)

// SequencerInfo holds a discovered sequencer's chain ID and its minimum fee.
type SequencerInfo struct {
	ChainID base.ChainID
	Fee     uint64
}

// SequencerRegistry maintains an up-to-date list of active sequencers.
// Thread-safe. Shared across all senders.
type SequencerRegistry struct {
	mu         sync.RWMutex
	sequencers []SequencerInfo
}

func NewSequencerRegistry() *SequencerRegistry {
	return &SequencerRegistry{}
}

// Refresh queries the node for active sequencers and updates the registry.
func (sr *SequencerRegistry) Refresh(clnt *client.APIClient) error {
	seqOutputs, _, err := clnt.GetAllSequencerOutputs()
	if err != nil {
		return err
	}

	var infos []SequencerInfo
	for chainID, seqOut := range seqOutputs {
		sd, err := ledger.ParseSequencerData(seqOut.Output)
		if err != nil {
			continue
		}
		fee := sd.MinimumFee()
		if fee == 0 {
			fee = 1
		}
		infos = append(infos, SequencerInfo{
			ChainID: chainID,
			Fee:     fee,
		})
	}

	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.sequencers = infos
	return nil
}

// Count returns the number of known sequencers.
func (sr *SequencerRegistry) Count() int {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	return len(sr.sequencers)
}

// snapshot returns a copy of the current sequencer list.
func (sr *SequencerRegistry) snapshot() []SequencerInfo {
	sr.mu.RLock()
	defer sr.mu.RUnlock()
	cp := make([]SequencerInfo, len(sr.sequencers))
	copy(cp, sr.sequencers)
	return cp
}

// SequencerPicker selects sequencers per-sender according to a strategy.
// Each sender gets its own picker with an independent random starting position.
type SequencerPicker struct {
	registry *SequencerRegistry
	strategy string // "next" or "random"
	nextIdx  int    // current position for round-robin
}

// NewSequencerPicker creates a picker for one sender.
// The initial position is randomized so different senders cycle through different sequencers.
func NewSequencerPicker(registry *SequencerRegistry, strategy string) *SequencerPicker {
	return &SequencerPicker{
		registry: registry,
		strategy: strategy,
		nextIdx:  rand.Intn(1<<30), // large random start; will be modded by list length
	}
}

// Next returns the next sequencer for this sender.
// Returns false if no sequencers are known.
func (p *SequencerPicker) Next() (SequencerInfo, bool) {
	seqs := p.registry.snapshot()
	if len(seqs) == 0 {
		return SequencerInfo{}, false
	}

	switch p.strategy {
	case StrategyRandom:
		return seqs[rand.Intn(len(seqs))], true
	default: // "next" — round-robin from sender's current position
		idx := p.nextIdx % len(seqs)
		p.nextIdx = idx + 1
		return seqs[idx], true
	}
}
