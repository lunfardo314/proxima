package txbuilder

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/yoseplee/vrf"
)

// MakeSequencerTransactionParams contains parameters for the sequencer transaction builder
type MakeSequencerTransactionParams struct {
	// sequencer name. By convention, can be <sequencer name>.<proposer name>
	SeqName string
	// predecessor
	ChainInput *ledger.OutputWithChainID
	//
	StemInput *ledger.OutputWithID // it is branch tx if != nil
	// timestamp of the transaction
	Timestamp ledger.Time
	// minimum fee
	MinimumFee uint64
	// additional inputs to consume. Must be unlockable by chain
	// can contain sender commands to the sequencer
	AdditionalInputs []*ledger.OutputWithID
	// additional outputs to produce
	AdditionalOutputs []*ledger.Output
	// Endorsements
	Endorsements []*ledger.TransactionID
	// chain controller
	PrivateKey ed25519.PrivateKey
	// PutInflation if true, calculates maximum inflation possible
	// if false, does not add inflation constraint at all
	PutInflation      bool
	ReturnInputLoader bool
}

func MakeSequencerTransaction(par MakeSequencerTransactionParams) ([]byte, error) {
	ret, _, err := MakeSequencerTransactionWithInputLoader(par)
	return ret, err
}

func MakeSequencerTransactionWithInputLoader(par MakeSequencerTransactionParams) ([]byte, func(i byte) (*ledger.Output, error), error) {
	var consumedOutputs []*ledger.Output
	if par.ReturnInputLoader {
		consumedOutputs = make([]*ledger.Output, 0)
	}
	errP := util.MakeErrFuncForPrefix("MakeSequencerTransaction")

	if !par.Timestamp.IsSlotBoundary() && !ledger.L().ID.IsPostBranchConsolidationTimestamp(par.Timestamp) {
		return nil, nil, errP("timestamp violates post-branch timestamp constraint: %s", par.Timestamp.String())
	}
	nIn := len(par.AdditionalInputs) + 1
	if par.StemInput != nil {
		nIn++
	}
	switch {
	case nIn > 256:
		return nil, nil, errP("too many inputs")
	case par.StemInput != nil && par.Timestamp.Tick() != 0:
		return nil, nil, errP("wrong timestamp for branch transaction: %s", par.Timestamp.String())
	case par.Timestamp.Slot() > par.ChainInput.ID.Slot() && par.Timestamp.Tick() != 0 && len(par.Endorsements) == 0:
		return nil, nil, errP("cross-slot sequencer tx must endorse another sequencer tx: chain input ts: %s, target: %s",
			par.ChainInput.ID.Timestamp(), par.Timestamp)
	case !par.ChainInput.ID.IsSequencerTransaction() && par.StemInput == nil && len(par.Endorsements) == 0:
		return nil, nil, errP("chain predecessor is not a sequencer transaction -> endorsement of sequencer transaction is mandatory (unless making a branch)")
	}

	chainInConstraint, chainInConstraintIdx := par.ChainInput.Output.ChainConstraint()
	if chainInConstraintIdx == 0xff {
		return nil, nil, errP("not a chain output: %s", par.ChainInput.ID.StringShort())
	}

	txb := NewTransactionBuilder()
	// count sums
	additionalIn, additionalOut := uint64(0), uint64(0)
	for _, o := range par.AdditionalInputs {
		additionalIn += o.Output.Amount()
	}
	for _, o := range par.AdditionalOutputs {
		additionalOut += o.Amount()
	}
	chainInAmount := par.ChainInput.Output.Amount()

	totalInAmount := chainInAmount + additionalIn
	if totalInAmount < additionalOut {
		return nil, nil, errP("not enough tokens in the input")
	}

	var inflationAmount uint64
	var inflationConstraint *ledger.InflationConstraint

	if par.PutInflation {
		inflationConstraint = &ledger.InflationConstraint{}
		inflationConstraint.ChainInflation, inflationConstraint.DelayedInflationIndex = calcChainInflationAmount(&par)

		if par.StemInput == nil {
			// calculate inflation value allowed in the context
			// non-branch transaction
			inflationAmount = inflationConstraint.ChainInflation
		} else {
			// branch transaction. Generate verifiable randomness. It will be used to deterministically calculate inflation amount
			pubKey := par.PrivateKey.Public().(ed25519.PublicKey)
			var err error

			util.AssertNotNil(par.StemInput)
			// using stem predecessor ID as msg for VRF to randomize branch inflation for the same sequencer even on the same slot
			inflationConstraint.VRFProof, _, err = vrf.Prove(pubKey, par.PrivateKey, par.StemInput.ID[:])
			if err != nil {
				return nil, nil, errP(err, "while generating VRF randomness proof")
			}

			{
				var ok bool
				// double check if VRF randomness proof has been generated correctly
				ok, err = vrf.Verify(pubKey, inflationConstraint.VRFProof, par.StemInput.ID[:])
				util.AssertNoError(err, "MakeSequencerTransactionWithInputLoader: verify VRF proof")
				util.Assertf(ok, "MakeSequencerTransactionWithInputLoader: verify VRF proof")
			}
			inflationAmount = ledger.L().BranchInflationBonusFromRandomnessProof(inflationConstraint.VRFProof)
		}
	}

	chainOutAmount := totalInAmount + inflationAmount - additionalOut // >= 0

	if chainOutAmount < ledger.L().Const().MinimumAmountOnSequencer() {
		return nil, nil, errP("amount on the chain output is below minimum required for the sequencer: %s",
			util.Th(ledger.L().Const().MinimumAmountOnSequencer()))
	}

	totalOutAmount := chainOutAmount + additionalOut
	util.Assertf(totalInAmount+inflationAmount == totalOutAmount, "totalInAmount == totalOutAmount")

	// make chain input/output
	chainPredIdx, err := txb.ConsumeOutput(par.ChainInput.Output, par.ChainInput.ID)
	if err != nil {
		return nil, nil, errP(err)
	}
	if par.ReturnInputLoader {
		consumedOutputs = append(consumedOutputs, par.ChainInput.Output)
	}
	txb.PutSignatureUnlock(chainPredIdx)

	seqID := chainInConstraint.ID
	if chainInConstraint.IsOrigin() {
		seqID = ledger.MakeOriginChainID(&par.ChainInput.ID)
	}

	var chainOutConstraintIdx byte

	chainOut := ledger.NewOutput(func(o *ledger.Output) {
		o.PutAmount(chainOutAmount)
		o.PutLock(par.ChainInput.Output.Lock())
		// put chain constraint
		chainOutConstraint := ledger.NewChainConstraint(seqID, chainPredIdx, chainInConstraintIdx, 0)
		chainOutConstraintIdx, _ = o.PushConstraint(chainOutConstraint.Bytes())
		// put sequencer constraint
		sequencerConstraint := ledger.NewSequencerConstraint(chainOutConstraintIdx, totalOutAmount)
		_, _ = o.PushConstraint(sequencerConstraint.Bytes())

		outData := ledger.ParseMilestoneData(par.ChainInput.Output)
		if outData == nil {
			outData = &ledger.MilestoneData{
				Name:         par.SeqName,
				MinimumFee:   par.MinimumFee,
				BranchHeight: 0,
				ChainHeight:  0,
			}
		} else {
			outData.ChainHeight += 1
			if par.StemInput != nil {
				outData.BranchHeight += 1
			}
			outData.Name = par.SeqName
		}
		// milestone data is on fixed index. For some reason
		idxMsData, _ := o.PushConstraint(outData.AsConstraint().Bytes())
		util.Assertf(idxMsData == ledger.MilestoneDataFixedIndex, "idxMsData == MilestoneDataFixedIndex")

		if inflationConstraint != nil {
			inflationConstraint.ChainConstraintIndex = chainOutConstraintIdx
			_, _ = o.PushConstraint(inflationConstraint.Bytes())
			//fmt.Printf(">>>>>>>>>>>>>>> push %s\n", inflationConstraint.String())
		}
	})

	chainOutIndex, err := txb.ProduceOutput(chainOut)
	if err != nil {
		return nil, nil, errP(err)
	}
	// unlock chain input (chain constraint unlock + inflation (optionally)
	txb.PutUnlockParams(chainPredIdx, chainInConstraintIdx, ledger.NewChainUnlockParams(chainOutIndex, chainOutConstraintIdx, 0))

	// make stem input/output if it is a branch transaction
	stemOutputIndex := byte(0xff)
	if par.StemInput != nil {
		_, err = txb.ConsumeOutput(par.StemInput.Output, par.StemInput.ID)
		if err != nil {
			return nil, nil, errP(err)
		}
		if par.ReturnInputLoader {
			consumedOutputs = append(consumedOutputs, par.StemInput.Output)
		}

		stemOut := ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(par.StemInput.Output.Amount())
			o.WithLock(&ledger.StemLock{
				PredecessorOutputID: par.StemInput.ID,
			})
		})
		stemOutputIndex, err = txb.ProduceOutput(stemOut)
		if err != nil {
			return nil, nil, errP(err)
		}
	}

	// consume and unlock additional inputs/outputs
	// unlock additional inputs
	tsIn := par.ChainInput.ID.Timestamp()
	for _, o := range par.AdditionalInputs {
		idx, err := txb.ConsumeOutput(o.Output, o.ID)
		if err != nil {
			return nil, nil, errP(err)
		}
		if par.ReturnInputLoader {
			consumedOutputs = append(consumedOutputs, o.Output)
		}
		switch lockName := o.Output.Lock().Name(); lockName {
		case ledger.AddressED25519Name:
			if err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0); err != nil {
				return nil, nil, err
			}
		case ledger.ChainLockName:
			txb.PutUnlockParams(idx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0, chainInConstraintIdx))
		default:
			return nil, nil, errP("unsupported type of additional input: %s", lockName)
		}
		tsIn = ledger.MaximumTime(tsIn, o.Timestamp())
	}

	if !ledger.ValidSequencerPace(tsIn, par.Timestamp) {
		return nil, nil, errP("timestamp %s is inconsistent with latest input timestamp %s", par.Timestamp.String(), tsIn.String())
	}

	_, err = txb.ProduceOutputs(par.AdditionalOutputs...)
	if err != nil {
		return nil, nil, errP(err)
	}
	txb.PushEndorsements(par.Endorsements...)
	txb.TransactionData.Timestamp = par.Timestamp
	txb.TransactionData.SequencerOutputIndex = chainOutIndex
	txb.TransactionData.StemOutputIndex = stemOutputIndex
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(par.PrivateKey)

	inputLoader := func(i byte) (*ledger.Output, error) {
		panic("MakeSequencerTransactionWithInputLoader: par.ReturnInputLoader parameter must be set to true")
	}
	if par.ReturnInputLoader {
		inputLoader = func(i byte) (*ledger.Output, error) {
			return consumedOutputs[i], nil
		}
	}
	return txb.TransactionData.Bytes(), inputLoader, nil
}

func calcChainInflationAmount(par *MakeSequencerTransactionParams) (uint64, byte) {
	delayedInflation := uint64(0)
	delayedInflationIdx := byte(0xff)
	if par.ChainInput.ID.IsBranchTransaction() {
		// take delayed inflation from predecessor
		var inflationConstraint *ledger.InflationConstraint
		inflationConstraint, delayedInflationIdx = par.ChainInput.Output.InflationConstraint()
		if delayedInflationIdx != 0xff {
			delayedInflation = inflationConstraint.ChainInflation
		}
	}
	ret, idx := ledger.L().CalcChainInflationAmount(par.ChainInput.Timestamp(), par.Timestamp, par.ChainInput.Output.Amount(), delayedInflation), delayedInflationIdx

	//fmt.Printf(">>>>>>>> [%s]: pred: %s, target: %s (ticks: %d), pred amount: %s, delayed amount: %s, less delayed: %s, return: %s\n",
	//	par.SeqName,
	//	par.ChainInput.IDShort(),
	//	par.Timestamp.String(),
	//	ledger.DiffTicks(par.Timestamp, par.ChainInput.Timestamp()),
	//	util.Th(par.ChainInput.Output.Amount()),
	//	util.Th(delayedInflation),
	//	util.Th(ret-delayedInflation),
	//	util.Th(ret),
	//)

	return ret, idx
}
