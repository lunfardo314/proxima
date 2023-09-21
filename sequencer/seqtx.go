package sequencer

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/txbuilder"
	"github.com/lunfardo314/proxima/util"
)

type MakeSequencerTransactionParams struct {
	// predecessor
	ChainInput *core.OutputWithChainID
	//
	StemInput *core.OutputWithID // it is branch tx if != nil
	// timestamp of the transaction
	Timestamp core.LogicalTime
	// minimum fee
	MinimumFee uint64
	// additional inputs to consume. Must be unlockable by chain
	// can contain sender commands to the sequencer
	AdditionalInputs []*core.OutputWithID
	// additional outputs to produce
	AdditionalOutputs []*core.Output
	// Endorsements
	Endorsements []*core.TransactionID
	// chain controller
	PrivateKey ed25519.PrivateKey
	//
	TotalSupply uint64
}

// Sequencer chain output has the following structure
// 0: amount constraint
// 1: AddressED25519 lock constraint
// 2: chain constraint
// 4: sequencer constraint

func MustValidSequencerOutput(chainOut *core.Output) {
	chainOut.MustValidOutput()
	switch chainOut.NumConstraints() {
	case 3:
		chainOut.MustHaveConstraintAnyOfAt(2, core.ChainConstraintName)
	case 4:
		chainOut.MustHaveConstraintAnyOfAt(2, core.ChainConstraintName)
		chainOut.MustHaveConstraintAnyOfAt(3, core.SequencerConstraintName)
	default:
		util.Panicf("3 or 4 constraints expected")
	}
}

func MakeSequencerTransaction(par MakeSequencerTransactionParams) ([]byte, error) {
	MustValidSequencerOutput(par.ChainInput.Output)

	errP := util.MakeErrFuncForPrefix("MakeSequencerTransaction")

	nIn := len(par.AdditionalInputs) + 1
	if par.StemInput != nil {
		nIn++
	}
	if nIn > 256 {
		return nil, errP("too many inputs")
	}
	if par.StemInput != nil && !par.ChainInput.ID.SequencerFlagON() {
		return nil, errP("chain input in the branch transaction must be a sequencer's output: %s", par.ChainInput.ID.Short())
	}
	if par.StemInput != nil && par.Timestamp.TimeTick() != 0 {
		return nil, errP("wrong timestamp for branch transaction: %s", par.Timestamp.String())
	}
	if par.Timestamp.TimeSlot() > par.ChainInput.ID.TimeSlot() && par.Timestamp.TimeTick() != 0 && len(par.Endorsements) == 0 {
		return nil, errP("cross-slot sequencer tx must endorse another sequencer tx: chain input ts: %s, target: %s",
			par.ChainInput.ID.Timestamp(), par.Timestamp)
	}
	txb := txbuilder.NewTransactionBuilder()
	// count sums
	additionalIn, additionalOut := uint64(0), uint64(0)
	for _, o := range par.AdditionalInputs {
		additionalIn += o.Output.Amount()
	}
	for _, o := range par.AdditionalOutputs {
		additionalOut += o.Amount()
	}
	chainInAmount := par.ChainInput.Output.Amount()

	// TODO safe arithmetics and checking against total supply etc

	chainOutAmount := chainInAmount + additionalIn - additionalOut // TODO this is temporary!!!!

	// make chain input/output
	chainConstraint, chainConstraintIdx := par.ChainInput.Output.ChainConstraint()
	if chainConstraintIdx == 0xff {
		return nil, errP("not a chain output: %s", par.ChainInput.ID.Short())
	}
	chainPredIdx, err := txb.ConsumeOutput(par.ChainInput.Output, par.ChainInput.ID)
	if err != nil {
		return nil, errP(err)
	}
	txb.PutSignatureUnlock(chainPredIdx)

	if !par.ChainInput.ID.SequencerFlagON() && len(par.Endorsements) == 0 {
		return nil, errP("chain predecessor is not a sequencer transaction -> endorsement of sequencer transaction is mandatory")
	}

	seqID := chainConstraint.ID
	if chainConstraint.IsOrigin() {
		seqID = core.OriginChainID(&par.ChainInput.ID)
	}

	chainConstraint = core.NewChainConstraint(seqID, chainPredIdx, chainConstraintIdx, 0)
	sequencerConstraint := core.NewSequencerConstraint(chainConstraintIdx, par.MinimumFee)

	chainOut := core.NewOutput(func(o *core.Output) {
		o.PutAmount(chainOutAmount)
		o.PutLock(par.ChainInput.Output.Lock())
		_, _ = o.PushConstraint(chainConstraint.Bytes())
		_, _ = o.PushConstraint(sequencerConstraint.Bytes())
	})
	MustValidSequencerOutput(chainOut)

	chainOutIndex, err := txb.ProduceOutput(chainOut)
	if err != nil {
		return nil, errP(err)
	}
	txb.PutUnlockParams(chainPredIdx, chainConstraintIdx, core.NewChainUnlockParams(chainOutIndex, chainConstraintIdx, 0))

	// make stem input/output if it is a branch transaction
	stemOutputIndex := byte(0xff)
	if par.StemInput != nil {
		stemInIndex, err := txb.ConsumeOutput(par.StemInput.Output, par.StemInput.ID)
		if err != nil {
			return nil, errP(err)
		}

		lck, ok := par.StemInput.Output.StemLock()
		if !ok {
			return nil, errP("can't find stem lock")
		}
		stemOut := core.NewOutput(func(o *core.Output) {
			o.WithAmount(par.StemInput.Output.Amount())
			o.WithLock(core.NewStemLock(lck.Supply, stemInIndex, par.StemInput.ID))
		})
		stemOutputIndex, err = txb.ProduceOutput(stemOut)
		if err != nil {
			return nil, errP(err)
		}
		txb.PutUnlockParams(stemInIndex, core.ConstraintIndexLock, []byte{stemOutputIndex})
	}

	// consume and unlock additional inputs/outputs
	// unlock additional inputs
	tsIn := core.MustNewLogicalTime(0, 0)
	for _, o := range par.AdditionalInputs {
		idx, err := txb.ConsumeOutput(o.Output, o.ID)
		if err != nil {
			return nil, errP(err)
		}
		switch lockName := o.Output.Lock().Name(); lockName {
		case core.AddressED25519Name:
			if err = txb.PutUnlockReference(idx, core.ConstraintIndexLock, 0); err != nil {
				return nil, err
			}
		case core.ChainLockName:
			txb.PutUnlockParams(idx, core.ConstraintIndexLock, core.NewChainLockUnlockParams(0, chainConstraintIdx))
		default:
			return nil, errP("unsupported type of additional input: %s", lockName)
		}
		tsIn = core.MaxLogicalTime(tsIn, o.Timestamp())
	}

	if !core.ValidTimePace(tsIn, par.Timestamp) {
		return nil, errP("timestamp inconsistent with inputs")
	}

	_, err = txb.ProduceOutputs(par.AdditionalOutputs...)
	if err != nil {
		return nil, errP(err)
	}
	txb.PushEndorsements(par.Endorsements...)
	txb.TransactionData.Timestamp = par.Timestamp
	txb.TransactionData.SequencerOutputIndex = chainOutIndex
	txb.TransactionData.StemOutputIndex = stemOutputIndex
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(par.PrivateKey)

	return txb.TransactionData.Bytes(), nil
}
