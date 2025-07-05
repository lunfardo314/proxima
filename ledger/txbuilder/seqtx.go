package txbuilder

import (
	"crypto/ed25519"
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
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
	Timestamp base.LedgerTime
	// minimum fee
	MinimumFee uint64
	// additional inputs to consume. Must be unlockable by chain
	// can contain sender commands to the sequencer
	AdditionalInputs []*ledger.OutputWithID
	// additional outputs to produce
	WithdrawOutputs []*ledger.Output
	// delegation outputs to transit
	DelegationOutputs []*ledger.OutputWithChainID
	// delegation inflation margin
	DelegationInflationMarginPromille int
	// Endorsements
	Endorsements []base.TransactionID
	// ExplicitBaseline or nil if none
	ExplicitBaseline *base.TransactionID
	// chain controller
	PrivateKey ed25519.PrivateKey
	// InflateMainChain if true, calculates maximum inflation possible on main chain transition
	// if false, does not add inflation constraint at all
	InflateMainChain bool
}

func MakeSequencerTransaction(par MakeSequencerTransactionParams) ([]byte, error) {
	ret, _, err := MakeSequencerTransactionWithInputLoader(par)
	return ret, err
}

func MakeSequencerTransactionWithInputLoader(par MakeSequencerTransactionParams) ([]byte, func(i byte) (*ledger.Output, error), error) {
	errP := util.MakeErrFuncForPrefix("MakeSequencerTransaction")

	if !par.Timestamp.IsSlotBoundary() && !ledger.L().ID.IsPostBranchConsolidationTimestamp(par.Timestamp) {
		return nil, nil, errP("timestamp violates post-branch timestamp constraint: %s", par.Timestamp.String())
	}
	nIn := len(par.AdditionalInputs) + len(par.DelegationOutputs) + 1
	if par.StemInput != nil {
		nIn++
	}

	if nIn > 256 {
		return nil, nil, errP("too many inputs. Max 256")
	}
	if par.StemInput != nil && par.Timestamp.Tick != 0 {
		return nil, nil, errP("wrong timestamp for branch transaction: %s", par.Timestamp.String())
	}
	if par.ExplicitBaseline == nil {
		if par.Timestamp.Slot > par.ChainInput.ID.Slot() && par.Timestamp.Tick != 0 && len(par.Endorsements) == 0 {
			return nil, nil, errP("cross-slot sequencer tx must endorse another sequencer tx: chain input ts: %s, target: %s",
				par.ChainInput.ID.Timestamp(), par.Timestamp)
		}
		if !par.ChainInput.ID.IsSequencerTransaction() && par.StemInput == nil && len(par.Endorsements) == 0 {
			return nil, nil, errP("chain predecessor is not a sequencer transaction -> endorsement of sequencer transaction is mandatory")
		}
	}

	txb := New()

	// calculate delegation outputs. Offset = 1 because inputs are consumed starting from index 1
	delegationTransitions, delegationTotalIn, delegationTotalOut, delegationMargin, err :=
		makeDelegationTransitions(par.DelegationOutputs, 1, par.Timestamp, par.DelegationInflationMarginPromille)
	if err != nil {
		return nil, nil, errP(fmt.Errorf("error while creating delegation transition: %w", err))
	}
	util.Assertf(delegationTotalIn <= delegationTotalOut+delegationMargin, "delegationTotalIn<=delegationTotalOut+delegationMargin")
	delegationInflation := delegationTotalOut - delegationTotalIn + delegationMargin

	// count sums of additional inputs and outputs
	additionalIn, withdrawOut := uint64(0), uint64(0)
	for _, o := range par.AdditionalInputs {
		additionalIn += o.Output.TokenBalance()
	}
	for _, o := range par.WithdrawOutputs {
		withdrawOut += o.TokenBalance()
	}

	var vrfProof []byte

	if par.StemInput != nil {
		// calculate VRF proof for the branch
		prevStem, ok := par.StemInput.Output.StemLock()
		if !ok {
			return nil, nil, errP(err, "inconsistency: cannot find previous stem")
		}

		// sign concatenation of predecessor VRFProof with slot number and next VRF proof
		msg := common.Concat(prevStem.VRFProof, par.Timestamp.Slot.Bytes())
		vrfProof = ed25519.Sign(par.PrivateKey, msg[:])
	}

	var mainChainInflationAmount uint64
	var mainChainInflationConstraint *ledger.InflationConstraint

	if par.InflateMainChain {
		// calculate main chain inflation amount
		if par.Timestamp.IsSlotBoundary() {
			// from VRF proof for branch
			util.Assertf(len(vrfProof) > 0, "len(vrfProof)>0")
			mainChainInflationAmount = ledger.L().BranchInflationBonusFromRandomnessProof(vrfProof)
		} else {
			// for non-branch
			mainChainInflationAmount = ledger.L().CalcChainInflationAmount(par.ChainInput.Timestamp(), par.Timestamp, par.ChainInput.Output.TokenBalance())
		}
		mainChainInflationConstraint = &ledger.InflationConstraint{
			InflationAmount: mainChainInflationAmount,
		}
	}

	// total input amount on the chain
	chainInAmount := par.ChainInput.Output.TokenBalance()

	// check if withdrawals are possible
	if chainInAmount+additionalIn+mainChainInflationAmount+delegationMargin < withdrawOut {
		return nil, nil, errP("not enough tokens on chain for withdrawal of %s", util.Th(withdrawOut))
	}

	// total input amount
	leftSideAmount := chainInAmount + additionalIn + delegationTotalIn

	// amount on produced chain output
	chainOutAmount := chainInAmount + additionalIn + mainChainInflationAmount + delegationMargin - withdrawOut // >= 0
	if chainOutAmount < ledger.L().ID.MinimumAmountOnSequencer {
		return nil, nil, errP("amount %s on the produced chain output is below minimum %s required for the sequencer",
			util.Th(chainOutAmount),
			util.Th(ledger.L().ID.MinimumAmountOnSequencer))
	}

	// total produced amount on transaction
	rightSideAmount := chainOutAmount + withdrawOut + delegationTotalOut
	// enforce consistency
	util.Assertf(leftSideAmount+mainChainInflationAmount+delegationInflation+delegationMargin == rightSideAmount,
		"leftSideAmount(%s)+mainChainInflationAmount(%s)+delegationInflation(%s)+delegationMargin(%s) == rightSideAmount(%s), diff: %d",
		util.Th(leftSideAmount), util.Th(mainChainInflationAmount), util.Th(delegationMargin), util.Th(delegationInflation), util.Th(rightSideAmount),
		int(leftSideAmount+mainChainInflationAmount+delegationMargin)-int(rightSideAmount),
	)

	// make main chain input/output
	chainPredIdx, err := txb.ConsumeOutput(par.ChainInput.Output, par.ChainInput.ID)
	if err != nil {
		return nil, nil, errP(err)
	}
	txb.PutSignatureUnlock(chainPredIdx)

	var chainOutConstraintIdx byte

	chainOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.PutTokenBalance(chainOutAmount)
		o.PutLock(par.ChainInput.Output.Lock())
		// put chain constraint
		chainOutConstraint := ledger.NewChainConstraint(par.ChainInput.ChainID, chainPredIdx, par.ChainInput.ChainConstraintIndex, par.ChainInput.OriginSlot, par.ChainInput.OriginAmount)
		chainOutConstraintIdx = o.MustPushConstraint(chainOutConstraint.Bytes())
		// put sequencer constraint
		sequencerConstraint := ledger.NewSequencerConstraint(chainOutConstraintIdx)
		o.MustPushConstraint(sequencerConstraint.Bytes())

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
		// milestone data is on fixed index. For some reason TODO
		idxMsData := o.MustPushConstraint(outData.AsConstraint().Bytes())
		util.Assertf(idxMsData == ledger.MilestoneDataFixedIndex, "idxMsData == MilestoneDataFixedIndex")

		if mainChainInflationConstraint != nil {
			mainChainInflationConstraint.ChainConstraintIndex = chainOutConstraintIdx
			o.MustPushConstraint(mainChainInflationConstraint.Bytes())
		}
	})

	chainOutIndex, err := txb.ProduceOutput(chainOut)
	if err != nil {
		return nil, nil, errP(err)
	}
	// unlock chain input (chain constraint unlock + inflation (optionally)
	txb.PutUnlockParams(chainPredIdx, par.ChainInput.ChainConstraintIndex, ledger.NewChainUnlockParams(chainOutIndex, chainOutConstraintIdx))

	// transit delegation outputs
	util.Assertf(len(par.DelegationOutputs) == len(delegationTransitions), "len(par.DelegationOutputs)==len(delegationTransitions)")
	for i, o := range par.DelegationOutputs {
		_, err = txb.ConsumeOutput(o.Output, o.ID)
		util.AssertNoError(err)
		txb.PutUnlockParams(byte(i+1), ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0, par.ChainInput.ChainConstraintIndex))

		succIdx, err := txb.ProduceOutput(delegationTransitions[i])
		util.AssertNoError(err)
		_, ccIdx := delegationTransitions[i].ChainConstraint()
		txb.PutUnlockParams(byte(i+1), ccIdx, ledger.NewChainUnlockParams(succIdx, ccIdx))
	}

	// make stem input/output if it is a branch transaction
	stemOutputIndex := byte(0xff)
	if par.StemInput != nil {
		_, err = txb.ConsumeOutput(par.StemInput.Output, par.StemInput.ID)
		if err != nil {
			return nil, nil, errP(err)
		}
		util.Assertf(len(vrfProof) > 0, "len(vrfProof)>0")

		stemOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(par.StemInput.Output.TokenBalance())
			o.WithLock(&ledger.StemLock{
				PredecessorOutputID: par.StemInput.ID,
				VRFProof:            vrfProof,
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
		switch lockName := o.Output.Lock().Name(); lockName {
		case ledger.AddressED25519Name:
			if err = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0); err != nil {
				return nil, nil, err
			}
		case ledger.ChainLockName:
			txb.PutUnlockParams(idx, ledger.ConstraintIndexLock, ledger.NewChainLockUnlockParams(0, par.ChainInput.ChainConstraintIndex))
		default:
			return nil, nil, errP("unsupported type of additional input: %s", lockName)
		}
		tsIn = base.MaximumTime(tsIn, o.Timestamp())
	}

	if !ledger.ValidSequencerPace(tsIn, par.Timestamp) {
		return nil, nil, errP("timestamp %s is inconsistent with latest input timestamp %s", par.Timestamp.String(), tsIn.String())
	}

	_, err = txb.ProduceOutputs(par.WithdrawOutputs...)
	if err != nil {
		return nil, nil, errP(err)
	}
	txb.PushEndorsements(par.Endorsements...)
	txb.PutExplicitBaseline(par.ExplicitBaseline)
	txb.TransactionData.Timestamp = par.Timestamp
	txb.TransactionData.SequencerOutputIndex = chainOutIndex
	txb.TransactionData.StemOutputIndex = stemOutputIndex
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(par.PrivateKey)

	txBytes := txb.TransactionData.Bytes()

	if err = transaction.ValidateTxBytes(txBytes, txb.LoadInput); err != nil {
		err = fmt.Errorf("%v\n-----------------------\n%s", err, transaction.LinesFromTransactionBytes(txBytes, txb.LoadInput).String())
		return nil, nil, errP("failed validate txBytes: %v", err)
	}

	return txBytes, txb.LoadInput, nil
}

func makeDelegationTransitions(inputs []*ledger.OutputWithChainID, offs byte, targetTs base.LedgerTime, delegationMarginPromille int) (
	ret []*ledger.Output,
	retTotalIn uint64,
	retTotalOut uint64,
	retMargin uint64,
	err error,
) {
	if len(inputs) == 0 {
		return
	}
	ret = make([]*ledger.Output, len(inputs))
	inflationTotal := uint64(0)

	for i, in := range inputs {
		cc, ccIdx := in.Output.ChainConstraint()
		if ccIdx == 0xff {
			err = fmt.Errorf("delegation output must be chain output")
			return
		}
		chainID := cc.ID
		if cc.IsOrigin() {
			chainID = base.MakeOriginChainID(in.ID)
		}
		if !ledger.IsOpenDelegationSlot(chainID, targetTs.Slot) {
			// only considering delegated outputs which can be consumed in the target slot
			err = fmt.Errorf("delegation is not open for %s: chainID: %s, oid: %s",
				targetTs.String(), chainID.StringShort(), in.ID.StringShort())
			return
		}

		inChainAmount := in.Output.TokenBalance()
		delegationInflation := ledger.L().CalcChainInflationAmount(in.ID.Timestamp(), targetTs, inChainAmount)

		inflationTotal += delegationInflation
		delegationMargin := uint64(delegationMarginPromille) * delegationInflation / 1000

		retTotalIn += inChainAmount
		retMargin += delegationMargin
		outChainAmount := inChainAmount + delegationInflation - delegationMargin
		retTotalOut += outChainAmount

		ret[i] = ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(outChainAmount).
				WithLock(in.Output.Lock())
			ccSucc := ledger.NewChainConstraint(chainID, byte(i)+offs, ccIdx, cc.OriginSlot, cc.OriginAmount)
			o.MustPushConstraint(ccSucc.Bytes())

			if delegationInflation > 0 {
				ic := ledger.InflationConstraint{
					InflationAmount:      delegationInflation,
					ChainConstraintIndex: ccIdx,
				}
				o.MustPushConstraint(ic.Bytes())
			}
		})
	}
	util.Assertf(retTotalOut == retTotalIn+inflationTotal, "retTotalOut == retTotalIn+inflationTotal")
	util.Assertf(retTotalIn+inflationTotal == retMargin+retTotalOut, "retTotalIn+inflationTotal == retMargin+retTotalOut")

	return ret, retTotalIn, retTotalOut, retMargin, nil
}
