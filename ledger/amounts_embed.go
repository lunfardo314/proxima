package ledger

import (
	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
)

// TODO in the future it makes sense to rewrite it all in EasyFL, for formal verifiability with TLA+ model
// TODO comment the logic of delegation in detail

// Inflation is validated by _validInflationAmount in chain.easyfl (EasyFL chain constraint).

// DelegateLock is a special case in amounts and inflation validation

//func evalAmounts(par *easyfl.CallParams[*EvalContext]) []byte {
//ctx := par.DataContext()
//if !ctx.SelfIsProducedOutput() {
//	// only enforce validity of amounts on produced outputs
//	return []byte{0xff}
//}
//o := ctx.SelfOutput()

//if o.Lock().Name() == DelegateLockName {
//	return evalEnforceFrozenCoverageOnDelegateOutput(par)
//}
//	return []byte{0xff}
//}

func evalTotalConsumed(par *easyfl.CallParams[*EvalContext]) []byte {
	idxBin := par.Arg(0)
	ret := easyfl_util.Uint64To8Bytes(uint64(par.DataContext().ConsumedTotal(idxBin[0])))
	return par.AllocData(ret[:]...)
}

func evalTotalProduced(par *easyfl.CallParams[*EvalContext]) []byte {
	idxBin := par.Arg(0)
	ret := easyfl_util.Uint64To8Bytes(uint64(par.DataContext().ProducedTotal(idxBin[0])))
	return par.AllocData(ret[:]...)
}
