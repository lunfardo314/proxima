package txutils

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/core"
	"golang.org/x/crypto/blake2b"
)

func ParseAndSortOutputData(outs []*core.OutputDataWithID, filter func(o *core.Output) bool, desc ...bool) ([]*core.OutputWithID, error) {
	ret := make([]*core.OutputWithID, 0, len(outs))
	for _, od := range outs {
		out, err := core.OutputFromBytesReadOnly(od.OutputData)
		if err != nil {
			return nil, err
		}
		if filter != nil && !filter(out) {
			continue
		}
		ret = append(ret, &core.OutputWithID{
			ID:     od.ID,
			Output: out,
		})
	}
	if len(desc) > 0 && desc[0] {
		sort.Slice(ret, func(i, j int) bool {
			return ret[i].Output.Amount() > ret[j].Output.Amount()
		})
	} else {
		sort.Slice(ret, func(i, j int) bool {
			return ret[i].Output.Amount() < ret[j].Output.Amount()
		})
	}
	return ret, nil
}

func FilterOutputsSortByAmount(outs []*core.OutputWithID, filter func(o *core.Output) bool, desc ...bool) []*core.OutputWithID {
	ret := make([]*core.OutputWithID, 0, len(outs))
	for _, out := range outs {
		if filter != nil && !filter(out.Output) {
			continue
		}
		ret = append(ret, out)
	}
	if len(desc) > 0 && desc[0] {
		sort.Slice(ret, func(i, j int) bool {
			return ret[i].Output.Amount() > ret[j].Output.Amount()
		})
	} else {
		sort.Slice(ret, func(i, j int) bool {
			return ret[i].Output.Amount() < ret[j].Output.Amount()
		})
	}
	return ret
}

func ParseAndSortOutputDataUpToAmount(outs []*core.OutputDataWithID, amount uint64, filter func(o *core.Output) bool, desc ...bool) ([]*core.OutputWithID, uint64, core.LogicalTime, error) {
	outsWitID, err := ParseAndSortOutputData(outs, filter, desc...)
	if err != nil {
		return nil, 0, core.NilLogicalTime, err
	}
	retTs := core.NilLogicalTime
	retSum := uint64(0)
	retOuts := make([]*core.OutputWithID, 0, len(outs))
	for _, o := range outsWitID {
		retSum += o.Output.Amount()
		retTs = core.MaxLogicalTime(retTs, o.Timestamp())
		retOuts = append(retOuts, o)
		if retSum >= amount {
			break
		}
	}
	if retSum < amount {
		return nil, 0, core.NilLogicalTime, fmt.Errorf("not enough tokens")
	}
	return retOuts, retSum, retTs, nil
}

func FilterChainOutputs(outs []*core.OutputWithID) ([]*core.OutputWithChainID, error) {
	ret := make([]*core.OutputWithChainID, 0)
	for _, o := range outs {
		ch, constraintIndex := o.Output.ChainConstraint()
		if constraintIndex == 0xff {
			continue
		}
		d := &core.OutputWithChainID{
			OutputWithID: core.OutputWithID{
				ID:     o.ID,
				Output: o.Output,
			},
			PredecessorConstraintIndex: constraintIndex,
		}
		if ch.IsOrigin() {
			h := blake2b.Sum256(o.ID[:])
			d.ChainID = h
		} else {
			d.ChainID = ch.ID
		}
		ret = append(ret, d)
	}
	return ret, nil
}

func forEachOutputReadOnly(outs []*core.OutputDataWithID, fun func(o *core.Output, odata *core.OutputDataWithID) bool) error {
	for _, odata := range outs {
		o, err := core.OutputFromBytesReadOnly(odata.OutputData)
		if err != nil {
			return err
		}
		if !fun(o, odata) {
			return nil
		}
	}
	return nil
}

func ParseChainConstraintsFromData(outs []*core.OutputDataWithID) ([]*core.OutputWithChainID, error) {
	ret := make([]*core.OutputWithChainID, 0)
	err := forEachOutputReadOnly(outs, func(o *core.Output, odata *core.OutputDataWithID) bool {
		ch, constraintIndex := o.ChainConstraint()
		if constraintIndex == 0xff {
			return true
		}
		d := &core.OutputWithChainID{
			OutputWithID: core.OutputWithID{
				ID:     odata.ID,
				Output: o,
			},
			PredecessorConstraintIndex: constraintIndex,
		}
		if ch.IsOrigin() {
			h := blake2b.Sum256(odata.ID[:])
			d.ChainID = h
		} else {
			d.ChainID = ch.ID
		}
		ret = append(ret, d)
		return true
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}
