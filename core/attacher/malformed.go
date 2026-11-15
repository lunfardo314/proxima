package attacher

import (
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
)

// CheckMalformed refuses a transaction with a token balance or inflation amount outside int64, or a
// branch whose stem oracle data does not decode. Full validation rejects such a transaction anyway;
// it is refused at ingress, before persist and gossip, and again before attachment. Node policy: the
// set of valid transactions does not change. It reads the sequencer data, so it runs after
// partial-context validation.
func CheckMalformed(tx *transaction.Transaction) (err error) {
	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, _ base.OutputID) bool {
		bin, e := o.At(int(ledger.ConstraintIndexAmounts))
		if e != nil {
			err = fmt.Errorf("output #%d: %v", idx, e)
			return false
		}
		amounts, e := ledger.AmountsFromBytes(bin)
		if e != nil {
			err = fmt.Errorf("output #%d: %v", idx, e)
			return false
		}
		if amounts.Amount(ledger.AmountIndexTokenBalance) < 0 || amounts.Amount(ledger.AmountIndexInflation) < 0 {
			err = fmt.Errorf("output #%d: amount out of range", idx)
			return false
		}
		return true
	})
	if err != nil {
		return err
	}
	if tx.IsBranchTransaction() {
		if _, ok := tx.StemOutput().Output.OracleData(); !ok {
			return fmt.Errorf("branch stem output carries no readable oracle data")
		}
	}
	return nil
}
