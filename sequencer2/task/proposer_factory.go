// f0 proposer: reads skeletons from the TransactionSkeletonFactory, inserts tag-along
// and delegation inputs, then proposes the result. Replaces e1/e2/e3/r2/r3 strategies.
//
// The factory runs as a persistent goroutine at the sequencer level, continuously producing
// skeletons with non-decreasing coverage. The f0 proposer drains the factory output,
// keeping the best skeleton. When the target deadline arrives, it takes the best skeleton,
// inserts inputs, and proposes it.

package task

import "github.com/lunfardo314/proxima/core/attacher"

const TraceTagFactoryProposer = "propose-f0"

func init() {
	registerProposerStrategy(&proposerStrategy{
		Name:             "factory0",
		ShortName:        "f0",
		GenerateProposal: factoryProposalGenerator,
	})
}

func factoryProposalGenerator(p *proposer) (*proposal, bool) {
	if p.targetTs.IsSlotBoundary() {
		// f0 does not generate branch transactions (b0 handles those)
		return nil, true
	}

	f := p.SkeletonFactory()
	if f == nil {
		return nil, true
	}

	// drain all available skeletons from factory, keep the best one
	var bestSkeleton *attacher.IncrementalAttacher
	var bestCoverage uint64

	for {
		select {
		case sk, ok := <-f.OutCh():
			if !ok {
				// factory channel closed
				goto done
			}
			if sk.Coverage >= bestCoverage {
				if bestSkeleton != nil {
					bestSkeleton.Close()
				}
				bestSkeleton = sk.IncrementalAttacher
				bestCoverage = sk.Coverage
			} else {
				sk.Close()
			}

		case <-p.ctx.Done():
			// target deadline reached — use what we have
			goto done

		default:
			// no more skeletons available right now — use what we have
			goto done
		}
	}

done:
	if bestSkeleton == nil {
		return nil, false
	}

	if !bestSkeleton.Completed() {
		bestSkeleton.Close()
		return nil, false
	}

	p.Tracef(TraceTagFactoryProposer, "using skeleton %s, coverage=%d, endorsements=%d",
		bestSkeleton.Name(), bestCoverage, len(bestSkeleton.Endorsing()))

	ret, err := p.newProposal(bestSkeleton)
	if err != nil {
		p.Tracef(TraceTagFactoryProposer, "failed to create proposal: %v", err)
		return nil, false
	}
	ret.insertInputs()

	if !ret.Completed() {
		ret.Close()
		return nil, false
	}

	// force exit: f0 produces one proposal per target (the best available skeleton)
	return ret, true
}
