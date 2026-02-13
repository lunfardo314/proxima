package ledger

import (
	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
)

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
