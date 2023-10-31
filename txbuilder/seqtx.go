package txbuilder

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
)

type (
	MakeSequencerTransactionParams struct {
		// sequencer name
		SeqName string
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
		//
		Inflation uint64
	}

	// MilestoneData data which is on sequencer as 'or(..)' constraint. It is not enforced by the ledger, yet maintained
	// by the sequencer
	MilestoneData struct {
		Name         string // < 256
		MinimumFee   uint64
		ChainHeight  uint32
		BranchHeight uint32
	}
)

func MakeSequencerTransaction(par MakeSequencerTransactionParams) ([]byte, error) {
	errP := util.MakeErrFuncForPrefix("MakeSequencerTransaction")

	nIn := len(par.AdditionalInputs) + 1
	if par.StemInput != nil {
		nIn++
	}
	switch{
	case nIn > 256:
		return nil, errP("too many inputs")
	case par.StemInput != nil && par.Timestamp.TimeTick() != 0:
		return nil, errP("wrong timestamp for branch transaction: %s", par.Timestamp.String())
	case par.Timestamp.TimeSlot() > par.ChainInput.ID.TimeSlot() && par.Timestamp.TimeTick() != 0 && len(par.Endorsements) == 0:
		return nil, errP("cross-slot sequencer tx must endorse another sequencer tx: chain input ts: %s, target: %s",
			par.ChainInput.ID.Timestamp(), par.Timestamp)
	case par.Inflation > 0 && par.StemInput == nil:
		return nil, errP("inflation must be 0 on non-branch sequencer transaction")
	case !par.ChainInput.ID.SequencerFlagON() && par.StemInput == nil && len(par.Endorsements) == 0:
		return nil, errP("chain predecessor is not a sequencer transaction -> endorsement of sequencer transaction is mandatory (unless making a branch)")
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

	// TODO safe arithmetics and checking against total supply etc. Temporary!!!!!
	totalProducedAmount := chainInAmount + additionalIn + par.Inflation
	chainOutAmount := totalProducedAmount - additionalOut

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

	seqID := chainConstraint.ID
	if chainConstraint.IsOrigin() {
		seqID = core.OriginChainID(&par.ChainInput.ID)
	}

	chainConstraint = core.NewChainConstraint(seqID, chainPredIdx, chainConstraintIdx, 0)
	sequencerConstraint := core.NewSequencerConstraint(chainConstraintIdx, totalProducedAmount)

	chainOut := core.NewOutput(func(o *core.Output) {
		o.PutAmount(chainOutAmount)
		o.PutLock(par.ChainInput.Output.Lock())
		_, _ = o.PushConstraint(chainConstraint.Bytes())
		_, _ = o.PushConstraint(sequencerConstraint.Bytes())
		outData := ParseMilestoneData(par.ChainInput.Output)
		if outData == nil {
			outData = &MilestoneData{
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
		_, _ = o.PushConstraint(outData.AsConstraint().Bytes())
		if par.Inflation > 0{
			core.NewInflationConstraint(par.Inflation, ???)
			// TODO
		}
	})

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

// ParseMilestoneData expected at index 4, otherwise nil
func ParseMilestoneData(o *core.Output) *MilestoneData {
	if o.NumConstraints() < 5 {
		return nil
	}
	ret, err := MilestoneDataFromConstraint(o.ConstraintAt(4))
	if err != nil {
		return nil
	}
	return ret
}

func (od *MilestoneData) AsConstraint() core.Constraint {
	dscrBin := []byte(od.Name)
	if len(dscrBin) > 255 {
		dscrBin = dscrBin[:256]
	}
	dscrBinStr := fmt.Sprintf("0x%s", hex.EncodeToString(dscrBin))
	chainIndexStr := fmt.Sprintf("u32/%d", od.ChainHeight)
	branchIndexStr := fmt.Sprintf("u32/%d", od.BranchHeight)
	minFeeStr := fmt.Sprintf("u64/%d", od.MinimumFee)

	src := fmt.Sprintf("or(%s)", strings.Join([]string{dscrBinStr, chainIndexStr, branchIndexStr, minFeeStr}, ","))
	_, _, bytecode, err := easyfl.CompileExpression(src)
	util.AssertNoError(err)

	constr, err := core.ConstraintFromBytes(bytecode)
	util.AssertNoError(err)

	return constr
}

func MilestoneDataFromConstraint(constr []byte) (*MilestoneData, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(constr)
	if err != nil {
		return nil, err
	}
	if sym != "or" {
		return nil, fmt.Errorf("sequencer.MilestoneDataFromConstraint: unexpected function '%s'", sym)
	}
	if len(args) != 4 {
		return nil, fmt.Errorf("sequencer.MilestoneDataFromConstraint: expected exactly 4 arguments, got %d", len(args))
	}
	dscrBin := easyfl.StripDataPrefix(args[0])
	chainIdxBin := easyfl.StripDataPrefix(args[1])
	branchIdxBin := easyfl.StripDataPrefix(args[2])
	minFeeBin := easyfl.StripDataPrefix(args[3])
	if len(chainIdxBin) != 4 || len(branchIdxBin) != 4 || len(minFeeBin) != 8 {
		return nil, fmt.Errorf("sequencer.MilestoneDataFromConstraint: unexpected argument sizes %d, %d, %d, %d",
			len(args[0]), len(args[1]), len(args[2]), len(args[3]))
	}
	return &MilestoneData{
		Name:         string(dscrBin),
		ChainHeight:  binary.BigEndian.Uint32(chainIdxBin),
		BranchHeight: binary.BigEndian.Uint32(branchIdxBin),
		MinimumFee:   binary.BigEndian.Uint64(minFeeBin),
	}, nil
}
