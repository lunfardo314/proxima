package ledger

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
)

/*self

All integers are treated big-endian. This way lexicographical order coincides with the arithmetic order

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

// MaxNumberOfEndorsements is equivalent to 2 parents in the original UTXOTangle.
// Here it can be any number from 0 to MaxNumberOfEndorsements inclusive
const MaxNumberOfEndorsements = 4

var _libraryAlreadyExtended bool

func extendLedgerWithBaseConstants(id *IdentityData) {
	// constants
	easyfl.Extend("vbCost16", "u16/1")
	//easyfl.Extend("ticksPerSlot", fmt.Sprintf("%d", id.TicksPerSlot()))
	easyfl.Extend("ticksPerSlot", fmt.Sprintf("%d", TicksPerSlot))
	easyfl.Extend("timeSlotSizeBytes", fmt.Sprintf("%d", SlotByteLength))
	easyfl.Extend("timestampByteSize", fmt.Sprintf("%d", TimeByteLength))
	easyfl.Extend("timePace", fmt.Sprintf("%d", TransactionPaceInTicks))
	easyfl.Extend("timePace64", fmt.Sprintf("u64/%d", TransactionPaceInTicks))

}

var (
	stateIdSingleton      *IdentityData
	stateIdSingletonMutex sync.Mutex
)

func Init(id *IdentityData) {
	//-------------------------------- standard EasyFL library extensions ------------------------------
	stateIdSingletonMutex.Lock()
	defer stateIdSingletonMutex.Unlock()

	tmp := stateIdSingleton
	if stateIdSingleton != nil {
		util.Assertf(stateIdSingleton.Hash() == id.Hash(), "library cannot be extended twice")
		return
	}
	stateIdSingleton = id
	tmp = tmp

	fmt.Printf("------ Base EasyFL library:\n")
	easyfl.PrintLibraryStats()
	defer func() {
		fmt.Printf("------ Extended EasyFL library:\n")
		easyfl.PrintLibraryStats()
	}()

	extendLedgerWithBaseConstants(id)

	// data context access
	// data context is a lazybytes.Tree
	easyfl.EmbedShort("@", 0, evalPath, true)
	// returns data bytes at the given path of the data context (lazy tree)
	easyfl.EmbedShort("@Path", 1, evalAtPath)

	// @Array8 interprets $0 as serialized LazyArray with max 256 elements. Takes the $1 element of it. $1 is expected 1-byte long
	easyfl.EmbedLong("@Array8", 2, evalAtArray8)
	// ArrayLength8 interprets $0 as serialized LazyArray with max 256 elements. Returns number of elements in the array as 1-byte value
	easyfl.EmbedLong("ArrayLength8", 1, evalNumElementsOfArray)

	// calls local EasyFL library
	easyfl.EmbedLong("callLocalLibrary", -1, evalCallLocalLibrary)

	// helpers
	easyfl.Extend("sizeIs", "equal(len8($0), $1)")
	easyfl.Extend("mustSize", "if(sizeIs($0,$1), $0, !!!wrong_data_size)")

	easyfl.EmbedLong("ticksBefore", 2, evalTicksBefore)
	easyfl.Extend("mustValidTimeTick", "if(and(mustSize($0,1),lessThan($0,ticksPerSlot)),$0,!!!wrong_timeslot)")
	easyfl.Extend("mustValidTimeSlot", "mustSize($0, timeSlotSizeBytes)")
	easyfl.Extend("timeSlotPrefix", "slice($0, 0, sub8(timeSlotSizeBytes,1))") // first 4 bytes of any array. It is not time slot yet
	easyfl.Extend("timeSlotFromTimeSlotPrefix", "bitwiseAND($0, 0x3fffffff)")
	easyfl.Extend("timeTickFromTimestamp", "byte($0, timeSlotSizeBytes)")
	easyfl.Extend("timestamp", "concat(mustValidTimeSlot($0),mustValidTimeTick($1))")

	{
		// inline tests
		easyfl.MustEqual("timestamp(u32/255, 21)", MustNewLedgerTime(255, 21).Hex())
		easyfl.MustEqual("ticksBefore(timestamp(u32/100, 5), timestamp(u32/101, 10))", "u64/105")
		easyfl.MustError("timestamp(u32/255, 100)", "wrong timeslot")
		easyfl.MustError("mustValidTimeSlot(255)", "wrong data size")
		easyfl.MustError("mustValidTimeTick(200)", "wrong timeslot")
		easyfl.MustEqual("mustValidTimeSlot(u32/255)", Slot(255).Hex())
		easyfl.MustEqual("mustValidTimeTick(88)", Tick(88).String())
	}

	// path constants
	easyfl.Extend("pathToTransaction", fmt.Sprintf("%d", TransactionBranch))
	easyfl.Extend("pathToConsumedOutputs", fmt.Sprintf("0x%s", PathToConsumedOutputs.Hex()))
	easyfl.Extend("pathToProducedOutputs", fmt.Sprintf("0x%s", PathToProducedOutputs.Hex()))
	easyfl.Extend("pathToUnlockParams", fmt.Sprintf("0x%s", PathToUnlockParams.Hex()))
	easyfl.Extend("pathToInputIDs", fmt.Sprintf("0x%s", PathToInputIDs.Hex()))
	easyfl.Extend("pathToSignature", fmt.Sprintf("0x%s", PathToSignature.Hex()))
	easyfl.Extend("pathToSeqAndStemOutputIndices", fmt.Sprintf("0x%s", PathToSequencerAndStemOutputIndices.Hex()))
	easyfl.Extend("pathToInputCommitment", fmt.Sprintf("0x%s", PathToInputCommitment.Hex()))
	easyfl.Extend("pathToEndorsements", fmt.Sprintf("0x%s", PathToEndorsements.Hex()))
	easyfl.Extend("pathToLocalLibrary", fmt.Sprintf("0x%s", PathToLocalLibraries.Hex()))
	easyfl.Extend("pathToTimestamp", fmt.Sprintf("0x%s", PathToTimestamp.Hex()))
	easyfl.Extend("pathToTotalProducedAmount", fmt.Sprintf("0x%s", PathToTotalProducedAmount.Hex()))

	// mandatory block indices in the output
	easyfl.Extend("amountConstraintIndex", fmt.Sprintf("%d", ConstraintIndexAmount))
	easyfl.Extend("lockConstraintIndex", fmt.Sprintf("%d", ConstraintIndexLock))

	// mandatory constraints and values
	// $0 is output binary as lazy array
	easyfl.Extend("amountConstraint", "@Array8($0, amountConstraintIndex)")
	easyfl.Extend("lockConstraint", "@Array8($0, lockConstraintIndex)")

	// recognize what kind of path is at $0
	easyfl.Extend("isPathToConsumedOutput", "hasPrefix($0, pathToConsumedOutputs)")
	easyfl.Extend("isPathToProducedOutput", "hasPrefix($0, pathToProducedOutputs)")

	// make branch path by index $0
	easyfl.Extend("consumedOutputPathByIndex", "concat(pathToConsumedOutputs,$0)")
	easyfl.Extend("unlockParamsPathByIndex", "concat(pathToUnlockParams,$0)")
	easyfl.Extend("producedOutputPathByIndex", "concat(pathToProducedOutputs,$0)")

	// takes 1-byte $0 as output index
	easyfl.Extend("consumedOutputByIndex", "@Path(consumedOutputPathByIndex($0))")
	easyfl.Extend("unlockParamsByIndex", "@Path(unlockParamsPathByIndex($0))")
	easyfl.Extend("producedOutputByIndex", "@Path(producedOutputPathByIndex($0))")

	// takes $0 'constraint index' as 2 bytes: 0 for output index, 1 for block index
	easyfl.Extend("producedConstraintByIndex", "@Array8(producedOutputByIndex(byte($0,0)), byte($0,1))")
	easyfl.Extend("consumedConstraintByIndex", "@Array8(consumedOutputByIndex(byte($0,0)), byte($0,1))")
	easyfl.Extend("unlockParamsByConstraintIndex", "@Array8(unlockParamsByIndex(byte($0,0)), byte($0,1))")

	easyfl.Extend("consumedLockByInputIndex", "consumedConstraintByIndex(concat($0, lockConstraintIndex))")

	easyfl.Extend("inputIDByIndex", "@Path(concat(pathToInputIDs,$0))")
	easyfl.Extend("timeSlotOfInputByIndex", "timeSlotFromTimeSlotPrefix(timeSlotPrefix(inputIDByIndex($0)))")

	// special transaction related

	easyfl.Extend("txBytes", "@Path(pathToTransaction)")
	easyfl.Extend("txSignature", "@Path(pathToSignature)")
	easyfl.Extend("txTimestampBytes", "@Path(pathToTimestamp)")
	easyfl.Extend("txTotalProducedAmount", "@Path(pathToTotalProducedAmount)")
	easyfl.Extend("txTimeSlot", "timeSlotPrefix(txTimestampBytes)")
	easyfl.Extend("txTimeTick", "timeTickFromTimestamp(txTimestampBytes)")
	easyfl.Extend("txSequencerOutputIndex", "byte(@Path(pathToSeqAndStemOutputIndices), 0)")
	easyfl.Extend("txStemOutputIndex", "byte(@Path(pathToSeqAndStemOutputIndices), 1)")
	easyfl.Extend("txEssenceBytes", "concat("+
		"@Path(pathToInputIDs), "+
		"@Path(pathToProducedOutputs), "+
		"@Path(pathToTimestamp), "+
		"@Path(pathToSeqAndStemOutputIndices), "+
		"@Path(pathToInputCommitment), "+
		"@Path(pathToEndorsements))")
	easyfl.Extend("isSequencerTransaction", "not(equal(txSequencerOutputIndex, 0xff))")
	easyfl.Extend("isBranchTransaction", "and(isSequencerTransaction, not(equal(txStemOutputIndex, 0xff)))")

	// endorsements
	easyfl.Extend("numEndorsements", "ArrayLength8(@Path(pathToEndorsements))")
	easyfl.Extend("sequencerFlagON", "not(isZero(bitwiseAND(byte($0,0),0x80)))")
	easyfl.Extend("branchFlagsON", "equal(bitwiseAND(byte($0,0),0xc0), 0xc0)")

	// functions with prefix 'self' are invocation context specific, i.e. they use function '@' to calculate
	// local values which depend on the invoked constraint

	easyfl.Extend("selfOutputPath", "slice(@,0,2)")
	easyfl.Extend("selfSiblingConstraint", "@Array8(@Path(selfOutputPath), $0)")
	easyfl.Extend("selfOutputBytes", "@Path(selfOutputPath)")
	easyfl.Extend("selfNumConstraints", "ArrayLength8(selfOutputBytes)")

	// unlock param branch (0 - transaction, 0 unlock params)
	// invoked output block
	easyfl.Extend("self", "@Path(@)")
	// bytecode prefix of the invoked constraint
	easyfl.Extend("selfBytecodePrefix", "parseBytecodePrefix(self)")

	easyfl.Extend("selfIsConsumedOutput", "isPathToConsumedOutput(@)")
	easyfl.Extend("selfIsProducedOutput", "isPathToProducedOutput(@)")

	// output index of the invocation
	easyfl.Extend("selfOutputIndex", "byte(@, 2)")
	// block index of the invocation
	easyfl.Extend("selfBlockIndex", "tail(@, 3)")
	// branch (2 bytes) of the constraint invocation
	easyfl.Extend("selfBranch", "slice(@,0,1)")
	// output index || block index
	easyfl.Extend("selfConstraintIndex", "slice(@, 2, 3)")
	// data of a constraint
	easyfl.Extend("constraintData", "tail($0,1)")
	// invocation output data
	easyfl.Extend("selfConstraintData", "constraintData(self)")
	// unlock parameters of the invoked consumed constraint
	easyfl.Extend("selfUnlockParameters", "@Path(concat(pathToUnlockParams, selfConstraintIndex))")
	// path referenced by the reference unlock params
	easyfl.Extend("selfReferencedPath", "concat(selfBranch, selfUnlockParameters, selfBlockIndex)")
	// returns unlock block of the sibling
	easyfl.Extend("selfSiblingUnlockBlock", "@Array8(@Path(concat(pathToUnlockParams, selfOutputIndex)), $0)")

	// returns selfUnlockParameters if blake2b hash of it is equal to the given hash, otherwise nil
	easyfl.Extend("selfHashUnlock", "if(equal($0, blake2b(selfUnlockParameters)),selfUnlockParameters,nil)")

	// takes ED25519 signature from full signature, first 64 bytes
	easyfl.Extend("signatureED25519", "slice($0, 0, 63)")
	// takes ED25519 public key from full signature
	easyfl.Extend("publicKeyED25519", "slice($0, 64, 95)")

	// init constraints
	initAmountConstraint()
	initAddressED25519Constraint()
	initDeadlineLockConstraint()
	initTimelockConstraint()
	initChainConstraint()
	initStemLockConstraint()
	initSequencerConstraint()
	initSenderED25519Constraint()
	initChainLockConstraint()
	initRoyaltiesED25519Constraint()
	initImmutableConstraint()
	initCommitToSiblingConstraint()
	initStateIndexConstraint()
	initTotalAmountConstraint()
	initInflationConstraint()

	runCommonUnitTests()

	libraryHash := easyfl.LibraryHash()
	fmt.Printf("Core constraint library hash is: %s\n", hex.EncodeToString(libraryHash[:]))
}

// for determinism in multiple tests
var startupLedgerTime = TimeNow()

// InitWithDefaultLedgerIDData for testing
func InitWithDefaultLedgerIDData(seed ...int) (ed25519.PrivateKey, *IdentityData) {
	s := 10000
	if len(seed) > 0 {
		s = seed[0]
	}
	pk := testutil.GetTestingPrivateKeys(1, s)
	id := DefaultIdentityData(pk[0], startupLedgerTime.Slot())
	Init(id)
	return pk[0], id
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

// CompileLocalLibrary compiles local library and serializes it as lazy array
func CompileLocalLibrary(source string) ([]byte, error) {
	libBin, err := easyfl.CompileLocalLibrary(source)
	if err != nil {
		return nil, err
	}
	ret := lazybytes.MakeArrayFromDataReadOnly(libBin...)
	return ret.Bytes(), nil
}

// arg 0 - local library binary (as lazy array)
// arg 1 - 1-byte index of then function in the library
// arg 2 ... arg 15 optional arguments
func evalCallLocalLibrary(ctx *easyfl.CallParams) []byte {
	arr := lazybytes.ArrayFromBytesReadOnly(ctx.Arg(0))
	libData := arr.Parsed()
	idx := ctx.Arg(1)
	if len(idx) != 1 || int(idx[0]) >= len(libData) {
		ctx.TracePanic("evalCallLocalLibrary: wrong function index")
	}
	ret := easyfl.CallLocalLibrary(ctx.Slice(2, ctx.Arity()), libData, int(idx[0]))
	ctx.Trace("evalCallLocalLibrary: lib#%d -> %s", idx[0], easyfl.Fmt(ret))
	return ret
}

// arg 0 and arg 1 are timestamps (5 bytes each)
// returns:
// nil, if ts1 is before ts0
// number of time slots between ts0 and ts1 otherwise as big-endian uint64
func evalTicksBefore(ctx *easyfl.CallParams) []byte {
	ts0bin, ts1bin := ctx.Arg(0), ctx.Arg(1)
	ts0, err := TimeFromBytes(ts0bin)
	if err != nil {
		ctx.TracePanic("evalTicksBefore: %v", err)
	}
	ts1, err := TimeFromBytes(ts1bin)
	if err != nil {
		ctx.TracePanic("evalTicksBefore: %v", err)
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
