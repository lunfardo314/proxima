package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"golang.org/x/crypto/blake2b"
)

type (
	// Output is an immutable UTXO: a tuple of constraint bytecodes.
	Output struct {
		*tuples.Tuple
	}

	// OutputBuilder is a mutable Output under construction.
	OutputBuilder struct {
		*tuples.TupleEditable
	}

	// OutputWithID pairs a parsed Output with its OutputID.
	OutputWithID struct {
		*Output
		ID base.OutputID
	}

	// OutputDataWithID pairs raw output bytes with their OutputID.
	OutputDataWithID struct {
		ID   base.OutputID
		Data []byte
	}

	// OutputDataWithChainID extends OutputDataWithID with the resolved ChainID.
	OutputDataWithChainID struct {
		OutputDataWithID
		ChainID base.ChainID
	}

	// OutputWithChainID is a parsed chain output with its ChainID and constraint metadata.
	OutputWithChainID struct {
		OutputWithID
		ChainConstraintData
	}

	// ChainConstraintData holds the parsed chain constraint and its index within the output tuple.
	ChainConstraintData struct {
		ChainConstraint
		ChainConstraintIndex byte
	}

	// SequencerOutputData holds parsed sequencer and chain constraint data for a sequencer output.
	SequencerOutputData struct {
		SequencerConstraint      *SequencerConstraint
		ChainConstraint          *ChainConstraint
		AmountOnChain            uint64
		SequencerConstraintIndex byte
		SequencerData            *seqdata.SequencerData
	}

	// OutputWithSequencerData is a parsed sequencer output with full sequencer metadata.
	OutputWithSequencerData struct {
		OutputWithID
		SequencerOutputData
	}
)

// NewOutput creates an Output by invoking buildFun on a fresh OutputBuilder.
func NewOutput(buildFun func(o *OutputBuilder)) *Output {
	arr := tuples.EmptyTupleEditable(256)
	builder := &OutputBuilder{arr}
	buildFun(builder)
	return &Output{arr.Tuple()}
}

// OutputBasic creates a minimal output with the given token amount and lock.
func OutputBasic(amount int64, lock Lock) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithAmounts(amount).WithLock(lock)
	})
}

// OutputBuilderFromBytes creates a mutable OutputBuilder from serialized output bytes.
func OutputBuilderFromBytes(data []byte) (*OutputBuilder, error) {
	ret, err := tuples.TupleFromBytesEditable(data, 256)
	if err != nil {
		return nil, fmt.Errorf("OutputBuilderFromBytes: %v", err)
	}
	return &OutputBuilder{ret}, nil
}

// OutputFromBytesMain parses an output and returns its amounts and lock using the latest library.
func OutputFromBytesMain(data []byte) (*Output, Amounts, Lock, error) {
	return OutputFromBytesMainWithLib(data, L(base.MaxSlot))
}

// OutputFromBytesMainWithLib parses an output and returns its amounts and lock.
func OutputFromBytesMainWithLib(data []byte, lib *Library) (*Output, Amounts, Lock, error) {
	arr, err := tuples.TupleFromBytes(bytes.Clone(data), 256)
	if err != nil {
		return nil, Amounts{}, nil, err
	}
	ret := &Output{arr}

	var amounts Amounts
	var lock Lock
	if ret.NumElements() < 2 {
		return nil, Amounts{}, nil, fmt.Errorf("at least 2 elements in the UTXO tuple are expected")
	}
	amountBin, err := ret.At(int(ConstraintIndexAmounts))
	if err != nil {
		return nil, Amounts{}, nil, err
	}
	if amounts, err = AmountsFromBytes(amountBin); err != nil {
		return nil, Amounts{}, nil, err
	}
	lockBin, err := ret.At(int(ConstraintIndexLock))
	if err != nil {
		return nil, Amounts{}, nil, err
	}
	if lock, err = LockFromBytesWithLib(lockBin, lib); err != nil {
		return nil, Amounts{}, nil, err
	}
	return ret, amounts, lock, nil
}

// OutputFromBytes parses an output from bytes using the latest library, with optional validation.
func OutputFromBytes(data []byte, validateOpt ...func(*Output) error) (*Output, error) {
	return OutputFromBytesWithLib(data, L(base.MaxSlot), validateOpt...)
}

// OutputFromBytesWithLib parses an output with optional validation using the given library.
func OutputFromBytesWithLib(data []byte, lib *Library, validateOpt ...func(*Output) error) (*Output, error) {
	ret, _, _, err := OutputFromBytesMainWithLib(data, lib)
	if err != nil {
		return nil, err
	}
	for _, validate := range validateOpt {
		if err = validate(ret); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

// OutputFromHexString parses an output from a hex-encoded string.
func OutputFromHexString(hexStr string, validateOpt ...func(*Output) error) (*Output, error) {
	data, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, err
	}
	return OutputFromBytes(data, validateOpt...)
}

// ConstraintsRawBytes returns raw bytecode of all constraints in the output tuple.
func (o *Output) ConstraintsRawBytes() [][]byte {
	ret := make([][]byte, o.NumConstraints())
	o.ForEach(func(i int, data []byte) bool {
		ret[i] = data
		return true
	})
	return ret
}

// StemLock returns the stem lock if the output has one.
func (o *Output) StemLock() (*StemLock, bool) {
	ret, ok := o.Lock().(*StemLock)
	return ret, ok
}

func (o *Output) MustStemLock() *StemLock {
	ret, ok := o.StemLock()
	util.Assertf(ok, "can't get stem output")
	return ret
}

// WithAmounts sets the amounts constraint on the output being built.
func (o *OutputBuilder) WithAmounts(amount ...int64) *OutputBuilder {
	o.MustPutAtIdxWithPadding(ConstraintIndexAmounts, NewAmounts(amount...).Bytes())
	return o
}

// WithTokenBalance sets a single token amount on the output being built.
func (o *OutputBuilder) WithTokenBalance(bal uint64) *OutputBuilder {
	return o.WithAmounts(int64(bal))
}

// Amounts returns the parsed amounts vector from the output.
func (o *Output) Amounts() Amounts {
	bin, err := o.At(int(ConstraintIndexAmounts))
	util.AssertNoError(err)
	ret, err := AmountsFromBytes(bin)
	util.AssertNoError(err)
	return ret
}

// TokenBalance returns the token balance (first element of the amounts vector).
func (o *Output) TokenBalance() uint64 {
	bin, err := o.At(int(ConstraintIndexAmounts))
	util.AssertNoError(err)
	ret, err := TokenBalanceFromAmountsBytes(bin)
	util.AssertNoError(err)
	return uint64(ret)
}

// FrozenCoverage returns the frozen coverage at index i (starting from index 2 of the amounts vector).
func (o *Output) FrozenCoverage(i byte) int64 {
	return o.Amounts().FrozenCoverageAt(i)
}

// InflatableAmount returns token balance plus the first frozen coverage, used for inflation calculation.
func (o *Output) InflatableAmount() uint64 {
	return o.TokenBalance() + uint64(o.FrozenCoverage(0))
}

// AdjustedFrozenCoverage returns the frozen coverage adjusted for elapsed epochs since the predecessor.
func (o *OutputWithChainID) AdjustedFrozenCoverage(txTs base.LedgerTime) int64 {
	predTs := o.ID.Timestamp()
	util.Assertf(txTs.AfterOrEqual(predTs), "txTs.AfterOrEqual(predTs)")
	lib := L(txTs.Slot)
	diff := lib.DiffEpochs(o.ChainID, txTs, o.ID.Timestamp())
	if diff >= int(lib.MaxFrozenEpochs) {
		return 0
	}
	return o.Output.FrozenCoverage(byte(diff))
}

// WithLock sets the lock constraint on the output being built.
func (o *OutputBuilder) WithLock(lock Lock) *OutputBuilder {
	o.PutConstraint(lock.Bytes(), ConstraintIndexLock)
	return o
}

// Hex returns the output bytes as a hex string.
func (o *Output) Hex() string {
	return hex.EncodeToString(o.Bytes())
}

// Clone creates a copy of the output, optionally applying modifications via buildFun.
func (o *Output) Clone(buildFun ...func(o *OutputBuilder)) *Output {
	if len(buildFun) == 0 {
		ret, err := OutputFromBytes(o.Bytes())
		util.AssertNoError(err)
		return ret
	}
	builder, err := OutputBuilderFromBytes(o.Bytes())
	util.AssertNoError(err)
	buildFun[0](builder)
	return &Output{builder.Tuple()}
}

// CloneRaw creates a byte-level copy without lock validation (for special outputs like upgrade UTXOs).
func (o *Output) CloneRaw() *Output {
	arr, err := tuples.TupleFromBytes(bytes.Clone(o.Bytes()), 256)
	util.AssertNoError(err)
	return &Output{arr}
}

// MustPushConstraint appends a constraint bytecode and returns its index. Panics if >= 256.
func (o *OutputBuilder) MustPushConstraint(c []byte) byte {
	util.Assertf(o.NumConstraints() < 256, "too many constraints")
	o.MustPush(c)
	return byte(o.NumElements() - 1)
}

// PutConstraint places constraint bytecode at the given index.
func (o *OutputBuilder) PutConstraint(c []byte, idx byte) {
	o.MustPutAtIdxWithPadding(idx, c)
}

// PutAmounts sets the amounts vector at constraint index 0.
func (o *OutputBuilder) PutAmounts(amount ...int64) {
	o.PutConstraint(NewAmounts(amount...).Bytes(), ConstraintIndexAmounts)
}

// PutLock sets the lock constraint at index 1.
func (o *OutputBuilder) PutLock(lock Lock) {
	o.PutConstraint(lock.Bytes(), ConstraintIndexLock)
}

// MustConstraintAt returns raw constraint bytecode at the given index. Panics if out of range.
func (o *Output) MustConstraintAt(idx byte) []byte {
	return o.MustAt(int(idx))
}

// ConstraintAt returns raw constraint bytecode at the given index.
func (o *Output) ConstraintAt(idx byte) ([]byte, error) {
	return o.At(int(idx))
}

func (o *OutputBuilder) NumConstraints() int {
	return o.NumElements()
}

func (o *Output) NumConstraints() int {
	return o.NumElements()
}

// Lock parses and returns the lock constraint at index 1.
func (o *Output) Lock() Lock {
	ret, err := LockFromBytes(o.MustAt(int(ConstraintIndexLock)))
	util.AssertNoError(err)
	return ret
}

// TimeLock returns the timelock slot if the output has a timelock constraint.
func (o *Output) TimeLock() (uint32, bool) {
	var ret Timelock
	var err error
	lib := L(base.MaxSlot)
	idx := o.IndexFunc(func(i int, data []byte) bool {
		if byte(i) < ConstraintIndexFirstOptionalConstraint {
			return false
		}
		ret, err = TimelockFromBytesWithLib(data, lib)
		return err == nil
	})
	if idx < 0 {
		return 0, false
	}
	return uint32(ret), true
}

// ChainConstraint finds and parses the chain constraint. Returns 0xff as index if not found.
func (o *Output) ChainConstraint() (*ChainConstraint, byte) {
	var ret *ChainConstraint
	var err error
	lib := L(base.MaxSlot)
	idx := o.IndexFunc(func(i int, data []byte) bool {
		if byte(i) < ConstraintIndexFirstOptionalConstraint {
			return false
		}
		ret, err = ChainConstraintFromBytesWithLib(data, lib)
		return err == nil
	})
	if idx < 0 {
		return nil, 0xff
	}
	return ret, byte(idx)
}

// SequencerConstraint finds and parses the sequencer constraint. Returns 0xff as index if not found.
func (o *Output) SequencerConstraint() (*SequencerConstraint, byte) {
	var ret *SequencerConstraint
	var err error
	lib := L(base.MaxSlot)
	idx := o.IndexFunc(func(idx int, data []byte) bool {
		if byte(idx) < ConstraintIndexFirstOptionalConstraint {
			return false
		}
		ret, err = SequencerConstraintFromBytesWithLib(data, lib)
		return err == nil
	})
	if idx < 0 {
		return nil, 0xff
	}
	return ret, byte(idx)
}

// IsSequencerOutput returns true if the output contains a sequencer constraint.
func (o *Output) IsSequencerOutput() bool {
	_, idx := o.SequencerConstraint()
	return idx != 0xff
}

// Inflation returns the inflation amount (index 1 in the amounts vector).
func (o *Output) Inflation() uint64 {
	return o.Amounts().InflationAmount()
}

// SequencerOutputData extracts sequencer and chain constraint data. Returns false if not a sequencer output.
func (o *Output) SequencerOutputData() (*SequencerOutputData, bool) {
	chainConstraint, chainConstraintIndex := o.ChainConstraint()
	if chainConstraintIndex == 0xff {
		return nil, false
	}
	var err error
	seqConstraintIndex := byte(0xff)
	var seqConstraint *SequencerConstraint
	lib := L(base.MaxSlot)
	idx := o.IndexFunc(func(i int, data []byte) bool {
		if byte(i) < ConstraintIndexFirstOptionalConstraint || byte(i) == chainConstraintIndex {
			return false
		}
		seqConstraint, err = SequencerConstraintFromBytesWithLib(data, lib)
		return err == nil
	})
	if idx < 0 {
		return nil, false
	}
	if seqConstraint.ChainConstraintIndex != chainConstraintIndex {
		return nil, false
	}

	var pSeqData *seqdata.SequencerData
	if seqData, err := ParseSequencerData(o); err == nil {
		pSeqData = &seqData
	}

	return &SequencerOutputData{
		SequencerConstraintIndex: seqConstraintIndex,
		SequencerConstraint:      seqConstraint,
		ChainConstraint:          chainConstraint,
		AmountOnChain:            o.TokenBalance(),
		SequencerData:            pSeqData,
	}, true
}

func (s *SequencerOutputData) Lines(prefix ...string) *lines.Lines {
	return s.SequencerData.Lines(prefix...)
}

// DelegationLock returns the DelegateLock if the output has one, otherwise nil.
func (o *Output) DelegationLock() *DelegateLock {
	lock := o.Lock()
	if lock.Name() != DelegateLockName {
		return nil
	}
	return lock.(*DelegateLock)
}

// EnsureStopDelegationConstraint finds the stop-delegation constraint. Returns 0xff as index if not found.
func (o *Output) EnsureStopDelegationConstraint() (*EnsureStopDelegation, byte) {
	var ret *EnsureStopDelegation
	var err error
	lib := L(base.MaxSlot)
	idx := o.IndexFunc(func(i int, data []byte) bool {
		if byte(i) < ConstraintIndexFirstOptionalConstraint {
			return false
		}
		ret, err = EnsureStopDelegationFromBytesWithLib(data, lib)
		return err == nil
	})
	if idx < 0 {
		return nil, 0xff
	}
	return ret, byte(idx)
}

// ToString returns a human-readable representation of the output.
func (o *Output) ToString(prefix ...string) string {
	return o.Lines(prefix...).String()
}

func (o *Output) ToSource(prefix ...string) string {
	return o.LinesSource(prefix...).String()
}

func (o *Output) Lines(prefix ...string) *lines.Lines {
	pref := ""
	if len(prefix) > 0 {
		pref = prefix[0]
	}
	return o._lines(pref, false, false)
}

func (o *Output) LinesSource(prefix ...string) *lines.Lines {
	pref := ""
	if len(prefix) > 0 {
		pref = prefix[0]
	}
	return o._lines(pref, true, false)
}

func (o *Output) LinesVerbose(prefix ...string) *lines.Lines {
	pref := ""
	if len(prefix) > 0 {
		pref = prefix[0]
	}
	return o._lines(pref, false, true)
}

func (o *Output) LinesHR(prefix ...string) *lines.Lines {
	pref := ""
	if len(prefix) > 0 {
		pref = prefix[0]
	}
	return o._lines(pref, false, false)
}

func (o *Output) String() string {
	return o.Lines().String()
}

// _lines formats all constraints as lines. If source=true, prints EasyFL source; if verbose=true, includes bytecode.
func (o *Output) _lines(prefix string, source bool, verbose bool) *lines.Lines {
	ret := lines.New()
	o.ForEach(func(i int, data []byte) bool {
		bc := ""
		if verbose {
			bc = fmt.Sprintf(prefix+"   bytecode: %s", easyfl_util.Fmt(data))
		}
		c, err := ConstraintFromBytesWithLib(data, L(base.MaxSlot))

		if err != nil {
			if src, err := L(base.MaxSlot).DecompileBytecode(data); err != nil {
				ret.Add("%s%d: bytecode=%s (%v)", prefix, i, hex.EncodeToString(data), err)
			} else {
				ret.Add("%s%d: bytecode=%s (len=%d)", prefix, i, src, len(data))
				if sm, err := base.SmallPersistentMapFromBytes(easyfl.StripDataPrefix(data)); err == nil {
					ret.Add("        parsed small map data -> " + sm.Lines().Join(", "))
				}
			}
		} else {
			if source {
				ret.Add("%s%d: %s%s", prefix, i, c.Source(), bc)
			} else {
				ret.Add("%s%d: %s%s", prefix, i, c.String(), bc)
			}
		}
		return true
	})
	return ret
}

// LinesPlainSource formats constraints as EasyFL source, with amounts shown as a parsed vector.
func (o *Output) LinesPlainSource() *lines.Lines {
	ret := lines.New()
	o.ForEach(func(i int, data []byte) bool {
		if byte(i) == ConstraintIndexAmounts {
			a, err := AmountsFromBytes(data)
			if err != nil {
				ret.Add(err.Error())
			} else {
				ret.Add("amounts" + a.String())
			}
			return true
		}
		c, err := ConstraintFromBytesWithLib(data, L(base.MaxSlot))
		if err != nil {
			ret.Add(err.Error())
		} else {
			ret.Add(c.Source())
		}
		return true
	})
	return ret
}

// LinesPlainHR formats constraints as human-readable strings, with amounts shown as a parsed vector.
func (o *Output) LinesPlainHR() *lines.Lines {
	ret := lines.New()
	o.ForEach(func(i int, data []byte) bool {
		if byte(i) == ConstraintIndexAmounts {
			a, err := AmountsFromBytes(data)
			if err != nil {
				ret.Add(err.Error())
			} else {
				ret.Add("amounts" + a.String())
			}
			return true
		}
		c, err := ConstraintFromBytesWithLib(data, L(base.MaxSlot))
		if err != nil {
			ret.Add(err.Error())
		} else {
			ret.Add(c.String())
		}
		return true
	})
	return ret
}

// Parse deserializes the raw output data into an OutputWithID, with optional validation.
func (o *OutputDataWithID) Parse(validOpt ...func(o *Output) error) (*OutputWithID, error) {
	ret, err := OutputFromBytes(o.Data, validOpt...)
	if err != nil {
		return nil, err
	}
	return &OutputWithID{
		ID:     o.ID,
		Output: ret,
	}, nil
}

// ParseAsChainOutput parses raw output data as a chain output. Returns the chain constraint index.
func (o *OutputDataWithID) ParseAsChainOutput() (*OutputWithChainID, byte, error) {
	var chainConstr *ChainConstraint
	var idx byte
	var chainID base.ChainID

	ret, err := o.Parse(func(oParsed *Output) error {
		chainConstr, idx = oParsed.ChainConstraint()
		if idx == 0xff {
			return fmt.Errorf("can't find chain constraint")
		}
		chainID = chainConstr.ChainID
		if chainID == base.NilChainID {
			chainID = blake2b.Sum256(o.ID[:])
		}
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return &OutputWithChainID{
		OutputWithID: *ret,
		ChainConstraintData: ChainConstraintData{
			ChainConstraint:      *chainConstr,
			ChainConstraintIndex: idx,
		},
	}, idx, nil
}

// MustParse is like Parse but panics on error.
func (o *OutputDataWithID) MustParse() *OutputWithID {
	ret, err := o.Parse()
	util.AssertNoError(err)
	return ret
}

// AsOutputWithChainID wraps an Output as OutputWithChainID if it has a chain constraint.
func AsOutputWithChainID(o *Output, oid base.OutputID) (OutputWithChainID, bool) {
	cData, ok := ExtractChainData(o, oid)
	if !ok {
		return OutputWithChainID{}, false
	}
	return OutputWithChainID{
		OutputWithID:        OutputWithID{ID: oid, Output: o},
		ChainConstraintData: cData,
	}, true
}

// ExtractChainData parses the chain constraint from an output. Resolves ChainID for origins via blake2b(oid).
func ExtractChainData(o *Output, oid base.OutputID) (chainConstraintData ChainConstraintData, ok bool) {
	cc, idx := o.ChainConstraint()
	if idx == 0xff {
		return ChainConstraintData{}, false
	}
	ret := ChainConstraintData{
		ChainConstraint:      *cc,
		ChainConstraintIndex: idx,
	}
	if cc.IsOrigin() {
		ret.ChainID = blake2b.Sum256(oid[:])
	}
	return ret, true
}

// ExtractChainID returns the ChainID, predecessor constraint index, and whether a chain constraint exists.
func (o *OutputWithID) ExtractChainID() (chainID base.ChainID, predecessorConstraintIndex byte, ok bool) {
	ret, ok := ExtractChainData(o.Output, o.ID)
	return ret.ChainID, ret.PredecessorInputIndex, ok
}

// AsChainOutput converts to OutputWithChainID, or returns error if not a chain output.
func (o *OutputWithID) AsChainOutput() (*OutputWithChainID, error) {
	cdata, ok := ExtractChainData(o.Output, o.ID)
	if !ok {
		return nil, fmt.Errorf("not a chain output")
	}
	return &OutputWithChainID{
		OutputWithID:        *o,
		ChainConstraintData: cdata,
	}, nil
}

// AsTagAlong wraps the output as a TagAlongOutput.
func (o *OutputWithID) AsTagAlong() TagAlongOutput {
	return TagAlongOutput{
		OutputWithID: *o,
		TagAlongLock: o.Output.TagAlongLock(),
	}
}

func (o *OutputWithID) MustAsChainOutput() *OutputWithChainID {
	ret, err := o.AsChainOutput()
	util.AssertNoError(err)
	return ret
}

func (o *OutputWithID) Timestamp() base.LedgerTime {
	return o.ID.Timestamp()
}

func (o *OutputWithID) Clone() *OutputWithID {
	return &OutputWithID{
		ID:     o.ID,
		Output: o.Output.Clone(),
	}
}

func (o *OutputWithID) LinesSource(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("id: %s, hex: %s", o.ID.String(), o.ID.StringHex())
	if cc, idx := o.Output.ChainConstraint(); idx != 0xff {
		var chainID base.ChainID
		if cc.IsOrigin() {
			chainID = blake2b.Sum256(o.ID[:])
		} else {
			chainID = cc.ChainID
		}
		ret.Add("      chainID: %s", chainID.String())
	}
	ret.Append(o.Output.LinesSource(prefix...))
	return ret
}

func (o *OutputWithID) LinesHR(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("id: %s, hex: %s", o.ID.String(), o.ID.StringHex())
	if cc, idx := o.Output.ChainConstraint(); idx != 0xff {
		var chainID base.ChainID
		if cc.IsOrigin() {
			chainID = blake2b.Sum256(o.ID[:])
		} else {
			chainID = cc.ChainID
		}
		ret.Add("      chainID: %s", chainID.String())
	}
	ret.Append(o.Output.LinesHR(prefix...))
	return ret
}

func (o *OutputWithID) String() string {
	return o.LinesHR().String()
}

func (o *OutputWithID) Source() string {
	return o.LinesSource().String()
}

func (o *OutputWithID) Short() string {
	return fmt.Sprintf("%s\n%s", o.ID.StringShort(), o.Output.ToString("   "))
}

func (o *OutputWithID) IDShort() string {
	return o.ID.StringShort()
}

// AdjustedTokenBalance returns the token balance adjusted for inflation at the output's slot.
func (o *OutputWithID) AdjustedTokenBalance() uint64 {
	return AdjustedAmount(o.TokenBalance(), o.ID.Slot())
}

// OutputsWithIDToString formats multiple outputs with their IDs and bytecode.
func OutputsWithIDToString(outs ...*OutputWithID) string {
	ret := lines.New()
	for i, o := range outs {
		ret.Add("%d : %s", i, o.ID.StringShort()).
			Add("      bytecode: %s", o.Output.Hex()).
			Append(o.Output.Lines("      "))
	}
	return ret.String()
}

// MustValidOutput panics if the lock constraint at index 1 is not parseable.
func (o *Output) MustValidOutput() {
	_, err := LockFromBytes(o.MustConstraintAt(1))
	util.AssertNoError(err)
}

// HashOutputs computes the blake2b hash of serialized outputs (used as input commitment).
func HashOutputs(outs ...*Output) [32]byte {
	arr := tuples.EmptyTupleEditable(256)
	for _, o := range outs {
		arr.MustPush(o.Bytes())
	}
	return blake2b.Sum256(arr.Bytes())
}

// ParseAndSortOutputData parses, filters, and sorts outputs by token balance (ascending by default).
func ParseAndSortOutputData(outs []*OutputDataWithID, filter func(oid *base.OutputID, o *Output) bool, desc ...bool) ([]*OutputWithID, error) {
	ret, err := ParseOutputDataAndFilter(outs, filter)
	if err != nil {
		return nil, err
	}
	if len(desc) > 0 && desc[0] {
		sort.Slice(ret, func(i, j int) bool {
			return ret[i].Output.TokenBalance() > ret[j].Output.TokenBalance()
		})
	} else {
		sort.Slice(ret, func(i, j int) bool {
			return ret[i].Output.TokenBalance() < ret[j].Output.TokenBalance()
		})
	}
	return ret, nil
}

// ParseOutputDataAndFilter parses raw output data and applies an optional filter.
func ParseOutputDataAndFilter(outs []*OutputDataWithID, filter func(oid *base.OutputID, o *Output) bool) ([]*OutputWithID, error) {
	ret := make([]*OutputWithID, 0, len(outs))
	lib := L(base.MaxSlot)
	for _, od := range outs {
		// Uses latest library version - upgrade code must maintain backward-compatible parsing
		out, err := OutputFromBytesWithLib(od.Data, lib)
		if err != nil {
			return nil, err
		}
		if filter != nil && !filter(&od.ID, out) {
			continue
		}
		ret = append(ret, &OutputWithID{
			ID:     od.ID,
			Output: out,
		})
	}
	return ret, nil
}

// FilterOutputsSortByAmount filters parsed outputs and sorts by token balance (ascending by default).
func FilterOutputsSortByAmount(outs []*OutputWithID, filter func(o *Output) bool, desc ...bool) []*OutputWithID {
	ret := make([]*OutputWithID, 0, len(outs))
	for _, out := range outs {
		if filter != nil && !filter(out.Output) {
			continue
		}
		ret = append(ret, out)
	}
	if len(desc) > 0 && desc[0] {
		sort.Slice(ret, func(i, j int) bool {
			return ret[i].Output.TokenBalance() > ret[j].Output.TokenBalance()
		})
	} else {
		sort.Slice(ret, func(i, j int) bool {
			return ret[i].Output.TokenBalance() < ret[j].Output.TokenBalance()
		})
	}
	return ret
}

// ParseAndSortOutputDataUpToAmount collects sorted outputs until the cumulative balance reaches amount.
func ParseAndSortOutputDataUpToAmount(outs []*OutputDataWithID, amount uint64, filter func(oid *base.OutputID, o *Output) bool, desc ...bool) ([]*OutputWithID, uint64, base.LedgerTime, error) {
	outsWitID, err := ParseAndSortOutputData(outs, filter, desc...)
	if err != nil {
		return nil, 0, base.NilLedgerTime, err
	}
	retTs := base.NilLedgerTime
	retSum := uint64(0)
	retOuts := make([]*OutputWithID, 0, len(outs))
	for _, o := range outsWitID {
		retSum += o.Output.TokenBalance()
		retTs = base.MaximumTime(retTs, o.Timestamp())
		retOuts = append(retOuts, o)
		if retSum >= amount {
			break
		}
	}
	if retSum < amount {
		return nil, 0, base.NilLedgerTime, fmt.Errorf("not enough tokens")
	}
	return retOuts, retSum, retTs, nil
}

// FilterChainOutputs returns only outputs that have a chain constraint, with resolved ChainIDs.
func FilterChainOutputs(outs []*OutputWithID) ([]*OutputWithChainID, error) {
	ret := make([]*OutputWithChainID, 0)
	for _, o := range outs {
		cc, constraintIndex := o.Output.ChainConstraint()
		if constraintIndex == 0xff {
			continue
		}
		d := &OutputWithChainID{
			OutputWithID: OutputWithID{
				ID:     o.ID,
				Output: o.Output,
			},
			ChainConstraintData: ChainConstraintData{
				ChainConstraint:      *cc,
				ChainConstraintIndex: constraintIndex,
			},
		}
		if cc.IsOrigin() {
			d.ChainID = blake2b.Sum256(o.ID[:])
		}
		ret = append(ret, d)
	}
	return ret, nil
}

// forEachOutputReadOnly parses each raw output and calls fun. Stops on first false return or error.
func forEachOutputReadOnly(outs []*OutputDataWithID, lib *Library, fun func(o *Output, odata *OutputDataWithID) bool) error {
	for _, odata := range outs {
		o, err := OutputFromBytesWithLib(odata.Data, lib)
		if err != nil {
			return err
		}
		if !fun(o, odata) {
			return nil
		}
	}
	return nil
}

// ParseChainConstraintsFromData parses raw outputs and returns those with chain constraints.
func ParseChainConstraintsFromData(outs []*OutputDataWithID) ([]*OutputWithChainID, error) {
	ret := make([]*OutputWithChainID, 0)
	err := forEachOutputReadOnly(outs, L(base.MaxSlot), func(o *Output, odata *OutputDataWithID) bool {
		ch, constraintIndex := o.ChainConstraint()
		if constraintIndex == 0xff {
			return true
		}
		d := &OutputWithChainID{
			OutputWithID: OutputWithID{
				ID:     odata.ID,
				Output: o,
			},
			ChainConstraintData: ChainConstraintData{
				ChainConstraint:      *ch,
				ChainConstraintIndex: constraintIndex,
			},
		}
		if ch.IsOrigin() {
			d.ChainID = blake2b.Sum256(odata.ID[:])
		}
		ret = append(ret, d)
		return true
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

const SeqMilestoneDataFixedIndex = 4

// ParseSequencerData parses the sequencer data from constraint index 4.
func ParseSequencerData(o *Output) (ret seqdata.SequencerData, err error) {
	if o.NumConstraints() <= SeqMilestoneDataFixedIndex {
		err = fmt.Errorf("ParseSequencerData: wrong number of constraints")
		return
	}
	data := easyfl.StripDataPrefix(o.MustConstraintAt(SeqMilestoneDataFixedIndex))
	if len(data) == 0 {
		return *seqdata.New(), nil
	}
	return seqdata.FromBytes(data)
}

// TagAlongLock returns the tag-along lock if the output has one, otherwise nil.
func (o *Output) TagAlongLock() *TagAlongLock {
	ret, err := TagAlongLockFromBytesWithLib(o.MustAt(int(ConstraintIndexLock)), L(base.MaxSlot))
	if err != nil {
		return nil
	}
	return ret
}

// EnoughAmountForStorageDeposit returns an error if the token balance is below the minimum storage deposit.
func (o *Output) EnoughAmountForStorageDeposit() error {
	m := MinimumStorageDeposit(o)
	if o.TokenBalance() >= m {
		return nil
	}
	return fmt.Errorf("not enough token balance (%s) for the minimum storage deposit (%s) in the output (size %d bytes):\n%s",
		util.Th(o.TokenBalance()), util.Th(m), len(o.Bytes()), o.LinesHR("     ").String())
}
