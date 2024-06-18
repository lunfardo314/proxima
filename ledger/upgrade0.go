package ledger

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/unitrie/common"
	"github.com/yoseplee/vrf"
)

// This file contains all upgrade prescriptions to the base ledger provided by the EasyFL. It is the "version 0" of the ledger.
// Ledger definition can be upgraded by adding new embedded and extended function with new binary codes.
// That will make ledger upgrades backwards compatible, because all past transactions and EasyFL constraint bytecodes
// outputs will be interpreted exactly the same way

/*
The following defines Proxima transaction model, library of constraints and other functions
in addition to the base library provided by EasyFL

All integers are treated big-endian. This way lexicographical order coincides with the arithmetic order.

The validation context is a tree-like data structure which is validated by evaluating all constraints in it
consumed and produced outputs. The rest of the validation should be done by the logic outside the data itself.
The tree-like data structure is a lazybytes.Array, treated as a tree.

Constants which define validation context data tree branches. Structure of the data tree:

(root)
  -- TransactionBranch = 0x00
       -- TxUnlockData = 0x00 (path 0x0000)  -- contains unlock params for each input
       -- TxInputIDs = 0x01     (path 0x0001)  -- contains up to 256 inputs, the IDs of consumed outputs
       -- TxOutputBranch = 0x02       (path 0x0002)  -- contains up to 256 produced outputs
       -- TxSignature = 0x03          (path 0x0003)  -- contains the only signature of the essence. It is mandatory
       -- TxTimestamp = 0x04          (path 0x0004)  -- mandatory timestamp of the transaction
       -- TxInputCommitment = 0x05    (path 0x0005)  -- blake2b hash of the all consumed outputs (which are under path 0x1000)
       -- TxEndorsements = 0x06       (path 0x0006)  -- list of transaction IDs of endorsed transaction
       -- TxLocalLibraries = 0x07     (path 0x0007)  -- list of local libraries in its binary form
  -- ConsumedBranch = 0x01
       -- ConsumedOutputsBranch = 0x00 (path 0x0100) -- all consumed outputs, up to 256

All consumed outputs ar contained in the tree element under path 0x0100
A input ID is at path 0x0001ii, where (ii) is 1-byte index of the consumed input in the transaction
This way:
	- the corresponding consumed output is located at path 0x0100ii (replacing 2 byte path prefix with 0x0100)
	- the corresponding unlock-parameters is located at path 0x0000ii (replacing 2 byte path prefix with 0x0000)
*/

// Top level branches
const (
	TransactionBranch = byte(iota)
	ConsumedBranch
)

// Transaction tree
const (
	TxUnlockData = byte(iota)
	TxInputIDs
	TxOutputs
	TxSignature
	TxSequencerAndStemOutputIndices
	TxTimestamp
	TxTotalProducedAmount
	TxInputCommitment
	TxEndorsements
	TxLocalLibraries
	TxTreeIndexMax
)

const (
	ConsumedOutputsBranch = byte(iota)
)

var (
	PathToConsumedOutputs               = lazybytes.Path(ConsumedBranch, ConsumedOutputsBranch)
	PathToProducedOutputs               = lazybytes.Path(TransactionBranch, TxOutputs)
	PathToUnlockParams                  = lazybytes.Path(TransactionBranch, TxUnlockData)
	PathToInputIDs                      = lazybytes.Path(TransactionBranch, TxInputIDs)
	PathToSignature                     = lazybytes.Path(TransactionBranch, TxSignature)
	PathToSequencerAndStemOutputIndices = lazybytes.Path(TransactionBranch, TxSequencerAndStemOutputIndices)
	PathToInputCommitment               = lazybytes.Path(TransactionBranch, TxInputCommitment)
	PathToEndorsements                  = lazybytes.Path(TransactionBranch, TxEndorsements)
	PathToLocalLibraries                = lazybytes.Path(TransactionBranch, TxLocalLibraries)
	PathToTimestamp                     = lazybytes.Path(TransactionBranch, TxTimestamp)
	PathToTotalProducedAmount           = lazybytes.Path(TransactionBranch, TxTotalProducedAmount)
)

// Mandatory output block indices
const (
	ConstraintIndexAmount = byte(iota)
	ConstraintIndexLock
	ConstraintIndexFirstOptionalConstraint
)

func (lib *Library) upgrade0(id *IdentityData) {
	lib.upgrade0WithEmbedded()
	lib.upgrade0WithExtensions(id)

}

//==================================== embedded

var (
	upgrade0EmbedFunctionsShort = []*easyfl.EmbeddedFunctionData{
		// data context access
		{"@", 0, evalPath},
		{"@Path", 1, evalAtPath},
	}
	upgrade0EmbedFunctionsLong = []*easyfl.EmbeddedFunctionData{
		{"@Array8", 2, evalAtArray8},
		{"ArrayLength8", 1, evalNumElementsOfArray},
		{"ticksBefore", 2, evalTicksBefore64},
		// TODO: replace Verifiable Random Function (VRF) with verified implementation, for example from Algorand
		// Parameters; publicKey, proof, message
		{"vrfVerify", 3, evalVRFVerify},
	}
)

func (lib *Library) upgrade0WithEmbedded() {
	lib.UpgradeWithEmbeddedShort(upgrade0EmbedFunctionsShort...)
	lib.UpgradeWthEmbeddedLong(upgrade0EmbedFunctionsLong...)
	lib.UpgradeWthEmbeddedLong(&easyfl.EmbeddedFunctionData{
		Sym:            "callLocalLibrary",
		RequiredNumPar: -1,
		EmbeddedFun:    lib.evalCallLocalLibrary,
	})
}

// DataContext is the data structure passed to the eval call. It contains:
// - tree: all validation context of the transaction, all data which is to be validated
// - path: a path in the validation context of the constraint being validated in the eval call
type DataContext struct {
	tree *lazybytes.Tree
	path lazybytes.TreePath
}

func NewDataContext(tree *lazybytes.Tree) *DataContext {
	return &DataContext{tree: tree}
}

func (c *DataContext) DataTree() *lazybytes.Tree {
	return c.tree
}

func (c *DataContext) Path() lazybytes.TreePath {
	return c.path
}

func (c *DataContext) SetPath(path lazybytes.TreePath) {
	c.path = common.Concat(path.Bytes())
}

// embedded functions

func evalPath(ctx *easyfl.CallParams) []byte {
	return ctx.DataContext().(*DataContext).Path()
}

func evalAtPath(ctx *easyfl.CallParams) []byte {
	return ctx.DataContext().(*DataContext).DataTree().BytesAtPath(ctx.Arg(0))
}

func evalAtArray8(ctx *easyfl.CallParams) []byte {
	arr := lazybytes.ArrayFromBytesReadOnly(ctx.Arg(0))
	idx := ctx.Arg(1)
	if len(idx) != 1 {
		panic("evalAtArray8: 1-byte value expected")
	}
	return arr.At(int(idx[0]))
}

func evalNumElementsOfArray(ctx *easyfl.CallParams) []byte {
	arr := lazybytes.ArrayFromBytesReadOnly(ctx.Arg(0))
	return []byte{byte(arr.NumElements())}
}

// evalVRFVerify: embedded VRF verifier. Dependency on unverified external crypto library
func evalVRFVerify(glb *easyfl.CallParams) []byte {
	var ok bool
	err := util.CatchPanicOrError(func() error {
		var err1 error
		ok, err1 = vrf.Verify(glb.Arg(0), glb.Arg(1), glb.Arg(2))
		return err1
	})
	if err != nil {
		glb.Trace("'vrfVerify embedded' failed with: %v", err)
	}
	if err == nil && ok {
		return []byte{0xff}
	}
	return nil
}

// CompileLocalLibrary compiles local library and serializes it as lazy array
func (lib *Library) CompileLocalLibrary(source string) ([]byte, error) {
	libBin, err := lib.Library.CompileLocalLibrary(source)
	if err != nil {
		return nil, err
	}
	ret := lazybytes.MakeArrayFromDataReadOnly(libBin...)
	return ret.Bytes(), nil
}

// arg 0 - local library binary (as lazy array)
// arg 1 - 1-byte index of then function in the library
// arg 2 ... arg 15 optional arguments
func (lib *Library) evalCallLocalLibrary(ctx *easyfl.CallParams) []byte {
	arr := lazybytes.ArrayFromBytesReadOnly(ctx.Arg(0))
	libData := arr.Parsed()
	idx := ctx.Arg(1)
	if len(idx) != 1 || int(idx[0]) >= len(libData) {
		ctx.TracePanic("evalCallLocalLibrary: wrong function index")
	}
	ret := lib.CallLocalLibrary(ctx.Slice(2, ctx.Arity()), libData, int(idx[0]))
	ctx.Trace("evalCallLocalLibrary: lib#%d -> %s", idx[0], easyfl.Fmt(ret))
	return ret
}

// arg 0 and arg 1 are timestamps (5 bytes each)
// returns:
// nil, if ts1 is before ts0
// number of ticks between ts0 and ts1 otherwise, as big-endian uint64
func evalTicksBefore64(ctx *easyfl.CallParams) []byte {
	ts0bin, ts1bin := ctx.Arg(0), ctx.Arg(1)
	ts0, err := TimeFromBytes(ts0bin)
	if err != nil {
		ctx.TracePanic("evalTicksBefore64: %v", err)
	}
	ts1, err := TimeFromBytes(ts1bin)
	if err != nil {
		ctx.TracePanic("evalTicksBefore64: %v", err)
	}
	diff := DiffTicks(ts1, ts0)
	if diff < 0 {
		// ts1 is before ts0
		return nil
	}
	var ret [8]byte
	binary.BigEndian.PutUint64(ret[:], uint64(diff))
	return ret[:]
}

//============================================ extensions

// upgrade0BaseConstants extension with base constants from ledger identity
func upgrade0BaseConstants(id *IdentityData) []*easyfl.ExtendedFunctionData {
	return []*easyfl.ExtendedFunctionData{
		{"constInitialSupply", fmt.Sprintf("u64/%d", id.InitialSupply)},
		{"constGenesisControllerPublicKey", fmt.Sprintf("0x%s", hex.EncodeToString(id.GenesisControllerPublicKey))},
		{"constGenesisTimeUnix", fmt.Sprintf("u64/%d", id.GenesisTimeUnix)},
		{"constTickDuration", fmt.Sprintf("u64/%d", int64(id.TickDuration))},
		{"constMaxTickValuePerSlot", fmt.Sprintf("u64/%d", id.MaxTickValueInSlot)},
		// begin inflation-related
		{"constBranchInflationBonusBase", fmt.Sprintf("u64/%d", id.BranchInflationBonusBase)},
		{"constChainInflationPerTickFraction", fmt.Sprintf("u64/%d", id.ChainInflationPerTickFraction)},
		{"constChainInflationOpportunitySlots", fmt.Sprintf("u64/%d", id.ChainInflationOpportunitySlots)},
		// end inflation-related
		{"constMinimumAmountOnSequencer", fmt.Sprintf("u64/%d", id.MinimumAmountOnSequencer)},
		{"constMaxNumberOfEndorsements", fmt.Sprintf("u64/%d", id.MaxNumberOfEndorsements)},

		{"constTransactionPace", fmt.Sprintf("u64/%d", id.TransactionPace)},
		{"constTransactionPaceSequencer", fmt.Sprintf("u64/%d", id.TransactionPaceSequencer)},
		{"constVBCost16", fmt.Sprintf("u16/%d", id.VBCost)}, // change to 64
		{"ticksPerSlot", fmt.Sprintf("%d", id.TicksPerSlot())},
		{"ticksPerSlot64", fmt.Sprintf("u64/%d", id.TicksPerSlot())},
		{"timeSlotSizeBytes", fmt.Sprintf("%d", SlotByteLength)},
		{"timestampByteSize", fmt.Sprintf("%d", TimeByteLength)},
	}
}

var upgrade0BaseHelpers = []*easyfl.ExtendedFunctionData{
	{"mustSize", "if(equalUint(len($0), $1), $0, !!!wrong_data_size)"},
	{"mustValidTimeTick", "if(and(mustSize($0,1),lessThan($0,ticksPerSlot)),$0,!!!wrong_timeslot)"},
	{"mustValidTimeSlot", "mustSize($0, timeSlotSizeBytes)"},
	{"timeSlotPrefix", "slice($0, 0, 3)"}, // first 4 bytes of any array. It is not time slot yet
	{"timeSlotFromTimeSlotPrefix", "bitwiseAND($0, 0x3fffffff)"},
	{"timeTickFromTimestamp", "byte($0, 4)"},
	{"timestamp", "concat(mustValidTimeSlot($0),mustValidTimeTick($1))"},
	// takes first 5 bytes and sets first 2 bit to zero
	{"timestampPrefix", "bitwiseAND(slice($0, 0, 4), 0x3fffffffff)"},
}

func (lib *Library) upgrade0WithBaseConstants(id *IdentityData) {
	lib.ID = id
	lib.UpgradeWithExtensions(upgrade0BaseConstants(id)...)
	lib.UpgradeWithExtensions(upgrade0BaseHelpers...)

	lib.appendInlineTests(func() {
		// inline tests
		libraryGlobal.MustEqual("timestamp(u32/255, 21)", MustNewLedgerTime(255, 21).Hex())
		libraryGlobal.MustEqual("ticksBefore(timestamp(u32/100, 5), timestamp(u32/101, 10))", "u64/105")
		libraryGlobal.MustError("timestamp(u32/255, 100)", "wrong timeslot")
		libraryGlobal.MustError("mustValidTimeSlot(255)", "wrong data size")
		libraryGlobal.MustError("mustValidTimeTick(200)", "wrong timeslot")
		libraryGlobal.MustEqual("mustValidTimeSlot(u32/255)", Slot(255).Hex())
		libraryGlobal.MustEqual("mustValidTimeTick(88)", Tick(88).String())
	})
}

func (lib *Library) upgrade0WithExtensions(id *IdentityData) *Library {
	lib.upgrade0WithBaseConstants(id)
	lib.upgrade0WithGeneralFunctions()
	lib.upgrade0WithConstraints()

	return lib
}

var upgrade0WithFunctions = []*easyfl.ExtendedFunctionData{
	{"pathToTransaction", fmt.Sprintf("%d", TransactionBranch)},
	{"pathToConsumedOutputs", fmt.Sprintf("0x%s", PathToConsumedOutputs.Hex())},
	{"pathToProducedOutputs", fmt.Sprintf("0x%s", PathToProducedOutputs.Hex())},
	{"pathToUnlockParams", fmt.Sprintf("0x%s", PathToUnlockParams.Hex())},
	{"pathToInputIDs", fmt.Sprintf("0x%s", PathToInputIDs.Hex())},
	{"pathToSignature", fmt.Sprintf("0x%s", PathToSignature.Hex())},
	{"pathToSeqAndStemOutputIndices", fmt.Sprintf("0x%s", PathToSequencerAndStemOutputIndices.Hex())},
	{"pathToInputCommitment", fmt.Sprintf("0x%s", PathToInputCommitment.Hex())},
	{"pathToEndorsements", fmt.Sprintf("0x%s", PathToEndorsements.Hex())},
	{"pathToLocalLibrary", fmt.Sprintf("0x%s", PathToLocalLibraries.Hex())},
	{"pathToTimestamp", fmt.Sprintf("0x%s", PathToTimestamp.Hex())},
	{"pathToTotalProducedAmount", fmt.Sprintf("0x%s", PathToTotalProducedAmount.Hex())},
	// mandatory block indices in the output
	{"amountConstraintIndex", fmt.Sprintf("%d", ConstraintIndexAmount)},
	{"lockConstraintIndex", fmt.Sprintf("%d", ConstraintIndexLock)},
	// mandatory constraints and values
	// $0 is output binary as lazy array
	{"amountConstraint", "@Array8($0, amountConstraintIndex)"},
	{"lockConstraint", "@Array8($0, lockConstraintIndex)"},
	// recognize what kind of path is at $0
	{"isPathToConsumedOutput", "hasPrefix($0, pathToConsumedOutputs)"},
	{"isPathToProducedOutput", "hasPrefix($0, pathToProducedOutputs)"},
	// make branch path by index $0
	{"consumedOutputPathByIndex", "concat(pathToConsumedOutputs,$0)"},
	{"unlockParamsPathByIndex", "concat(pathToUnlockParams,$0)"},
	{"producedOutputPathByIndex", "concat(pathToProducedOutputs,$0)"},
	// takes 1-byte $0 as output index
	{"consumedOutputByIndex", "@Path(consumedOutputPathByIndex($0))"},
	{"unlockParamsByIndex", "@Path(unlockParamsPathByIndex($0))"},
	{"producedOutputByIndex", "@Path(producedOutputPathByIndex($0))"},
	// takes $0 'constraint index' as 2 bytes: 0 for output index, 1 for block index
	{"producedConstraintByIndex", "@Array8(producedOutputByIndex(byte($0,0)), byte($0,1))"},
	{"consumedConstraintByIndex", "@Array8(consumedOutputByIndex(byte($0,0)), byte($0,1))"},
	{"unlockParamsByConstraintIndex", "@Array8(unlockParamsByIndex(byte($0,0)), byte($0,1))"},

	{"consumedLockByInputIndex", "consumedConstraintByIndex(concat($0, lockConstraintIndex))"},
	{"inputIDByIndex", "@Path(concat(pathToInputIDs,$0))"},
	{"timeSlotOfInputByIndex", "timeSlotFromTimeSlotPrefix(timeSlotPrefix(inputIDByIndex($0)))"},
	{"timestampOfInputByIndex", "timestampPrefix(inputIDByIndex($0))"},
	// special transaction related
	{"txBytes", "@Path(pathToTransaction)"},
	{"txSignature", "@Path(pathToSignature)"},
	{"txTimestampBytes", "@Path(pathToTimestamp)"},
	{"txTotalProducedAmount", "@Path(pathToTotalProducedAmount)"},
	{"txTimeSlot", "timeSlotPrefix(txTimestampBytes)"},
	{"txTimeTick", "timeTickFromTimestamp(txTimestampBytes)"},
	{"txSequencerOutputIndex", "byte(@Path(pathToSeqAndStemOutputIndices), 0)"},
	{"txStemOutputIndex", "byte(@Path(pathToSeqAndStemOutputIndices), 1)"},
	{"txEssenceBytes", "concat(" +
		"@Path(pathToInputIDs), " +
		"@Path(pathToProducedOutputs), " +
		"@Path(pathToTimestamp), " +
		"@Path(pathToSeqAndStemOutputIndices), " +
		"@Path(pathToInputCommitment), " +
		"@Path(pathToEndorsements))"},
	{"isSequencerTransaction", "not(equal(txSequencerOutputIndex, 0xff))"},
	{"isBranchTransaction", "and(isSequencerTransaction, not(equal(txStemOutputIndex, 0xff)))"},
	// endorsements
	{"numEndorsements", "ArrayLength8(@Path(pathToEndorsements))"},
	{"sequencerFlagON", "not(isZero(bitwiseAND(byte($0,0),0x80)))"},
	// functions with prefix 'self' are invocation context specific, i.e. they use function '@' to calculate
	// local values which depend on the invoked constraint
	{"selfOutputPath", "slice(@,0,2)"},
	{"selfSiblingConstraint", "@Array8(@Path(selfOutputPath), $0)"},
	{"selfOutputBytes", "@Path(selfOutputPath)"},
	{"selfNumConstraints", "ArrayLength8(selfOutputBytes)"},
	// unlock param branch (0 - transaction, 0 unlock params)
	// invoked output block
	{"self", "@Path(@)"},
	// bytecode prefix of the invoked constraint
	{"selfBytecodePrefix", "parsePrefixBytecode(self)"},
	{"selfIsConsumedOutput", "isPathToConsumedOutput(@)"},
	{"selfIsProducedOutput", "isPathToProducedOutput(@)"},
	// output index of the invocation
	{"selfOutputIndex", "byte(@, 2)"},
	// block index of the invocation
	{"selfBlockIndex", "tail(@, 3)"},
	// branch (2 bytes) of the constraint invocation
	{"selfBranch", "slice(@,0,1)"},
	// output index || block index
	{"selfConstraintIndex", "slice(@, 2, 3)"},
	// data of a constraint
	{"constraintData", "tail($0,1)"},
	// invocation output data
	{"selfConstraintData", "constraintData(self)"},
	// unlock parameters of the invoked consumed constraint
	{"selfUnlockParameters", "@Path(concat(pathToUnlockParams, selfConstraintIndex))"},
	// path referenced by the reference unlock params
	{"selfReferencedPath", "concat(selfBranch, selfUnlockParameters, selfBlockIndex)"},
	// returns unlock block of the sibling
	{"selfSiblingUnlockBlock", "@Array8(@Path(concat(pathToUnlockParams, selfOutputIndex)), $0)"},
	// returns selfUnlockParameters if blake2b hash of it is equal to the given hash, otherwise nil
	{"selfHashUnlock", "if(equal($0, blake2b(selfUnlockParameters)),selfUnlockParameters,nil)"},
	// takes ED25519 signature from full signature, first 64 bytes
	{"signatureED25519", "slice($0, 0, 63)"},
	// takes ED25519 public key from full signature
	{"publicKeyED25519", "slice($0, 64, 95)"},
}

func (lib *Library) upgrade0WithGeneralFunctions() {
	lib.UpgradeWithExtensions(upgrade0WithFunctions...)
}

func (lib *Library) upgrade0WithConstraints() {
	addAmountConstraint(lib)
	addAddressED25519Constraint(lib)
	addConditionalLock(lib)
	addDeadlineLockConstraint(lib)
	addTimeLockConstraint(lib)
	addChainConstraint(lib)
	addStemLockConstraint(lib)
	addSequencerConstraint(lib)
	addInflationConstraint(lib)
	addSenderED25519Constraint(lib)
	addChainLockConstraint(lib)
	//addRoyaltiesED25519Constraint(lib)
	addImmutableConstraint(lib)
	addCommitToSiblingConstraint(lib)
	//addTotalAmountConstraint(lib)
}
