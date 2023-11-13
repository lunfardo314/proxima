package utangle

import (
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util/lines"
)

func newPastTrack() PastTrack {
	return PastTrack{
		forks: newForkSet(),
	}
}

// absorbPastTrack merges branches and forks of vid into the pas track. In case a conflict is detected,
// the target PastTrack is left inconsistent and must be abandoned
func (p *PastTrack) absorbPastTrack(vid *WrappedTx, getStore func() general.StateStore) (conflict *WrappedOutput) {
	return p._absorbPastTrack(vid, getStore, false)
}

// AbsorbPastTrackSafe same as absorbPastTrack but leaves target untouched in case conflict is detected.
// It copies the target, so it somehow slower
func (p *PastTrack) AbsorbPastTrackSafe(vid *WrappedTx, getStore func() general.StateStore) (conflict *WrappedOutput) {
	return p._absorbPastTrack(vid, getStore, true)
}

func (p *PastTrack) _absorbPastTrack(vid *WrappedTx, getStore func() general.StateStore, safe bool) (conflict *WrappedOutput) {
	var success bool
	var baselineBranch *WrappedTx
	var wrappedConflict WrappedOutput

	if vid.IsBranchTransaction() {
		baselineBranch, success = mergeBranches(p.baselineBranch, vid, getStore)
	} else {
		baselineBranch, success = mergeBranches(p.baselineBranch, vid.BaselineBranch(), getStore)
	}
	if !success {
		conflict = &WrappedOutput{}
		return
	}

	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		if safe {
			wrappedConflict = p.forks.absorbSafe(v.pastTrack.forks)
		} else {
			wrappedConflict = p.forks.absorb(v.pastTrack.forks)
		}
		if wrappedConflict.VID != nil {
			conflict = &wrappedConflict
			return
		}
	}})
	if conflict == nil {
		p.baselineBranch = baselineBranch
	}
	return
}

func (p *PastTrack) BaselineBranch() *WrappedTx {
	return p.baselineBranch
}

func (p *PastTrack) MustGetBaselineState(ut *UTXOTangle) general.IndexedStateReader {
	return ut.MustGetBaselineState(p.BaselineBranch())
}

func (p *PastTrack) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	if p == nil {
		ret.Add("<nil>")
	} else {
		if p.baselineBranch == nil {
			ret.Add("----- baseline branch: <nil>")
		} else {
			ret.Add("----- baseline branch: %s", p.baselineBranch.IDShort())
		}
		ret.Add("---- forks")
		ret.Append(p.forks.lines())
	}
	return ret
}
