package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

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

	// ChainConstraintData holds the parsed chain constraint.
	ChainConstraintData struct {
		ChainConstraint
	}

	// SequencerOutputData holds parsed sequencer and chain constraint data for a sequencer output.
	SequencerOutputData struct {
		SequencerConstraint *SequencerConstraint
		ChainConstraint     *ChainConstraint
		AmountOnChain       uint64
		SequencerData       *seqdata.SequencerData
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

// OutputFromBytes does a structural-only parse of an output. Without
// validateOpt it does NOT require the ledger library to be initialised:
// it only checks that the outer tuple decodes, has at least 3 elements
// (`amounts | index-values | lock`), element 0 (amounts) decodes as a
// sub-tuple, and element 1 (index-values) is empty or decodes as a
// sub-tuple. Element 2 (lock bytecode) is present (the NumElements
// check guarantees this) but not decoded — lock dispatch is library-
// dependent and only happens through the WithLockParsed* hooks or via
// on-demand methods like Output.Lock().
//
// validateOpt funcs run after the structural check and can pull in
// heavier parsing (amounts vector validation, lock dispatch, …)
// including library-dependent steps.
//
// Trusted-bytes callers (output read back from the local txstore /
// state trie / a builder round-trip) typically don't need validateOpt:
// downstream methods (Output.Lock, Output.Amounts) parse on demand
// and panic on the impossible case of malformed bytes.
//
// Untrusted-bytes callers (incoming peer data, HTTP requests) should
// pass WithFullValidation() (or WithLockParsed() / WithAmountsParsed()
// individually) to surface bad input as an error here rather than
// later as a panic from the on-demand methods.
func OutputFromBytes(data []byte, validateOpt ...func(*Output) error) (*Output, error) {
	arr, err := tuples.TupleFromBytes(bytes.Clone(data), 256)
	if err != nil {
		return nil, fmt.Errorf("OutputFromBytes: %w", err)
	}
	ret := &Output{arr}
	if ret.NumElements() < 3 {
		return nil, fmt.Errorf("OutputFromBytes: at least 3 elements required (amounts | index-values | lock), got %d", ret.NumElements())
	}
	amountsBin, err := ret.At(int(ConstraintIndexAmounts))
	if err != nil {
		return nil, fmt.Errorf("OutputFromBytes: %w", err)
	}
	if _, err = tuples.TupleFromBytes(amountsBin, 256); err != nil {
		return nil, fmt.Errorf("OutputFromBytes: amounts at index 0 not a valid sub-tuple: %w", err)
	}
	ivBin, err := ret.At(int(ConstraintIndexIndexValues))
	if err != nil {
		return nil, fmt.Errorf("OutputFromBytes: %w", err)
	}
	if len(ivBin) > 0 {
		if _, err = tuples.TupleFromBytes(ivBin, 256); err != nil {
			return nil, fmt.Errorf("OutputFromBytes: index-values at index 1 not a valid sub-tuple: %w", err)
		}
	}
	for _, validate := range validateOpt {
		if err = validate(ret); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

// OutputFromBytesWithLib parses an output and runs full validation
// (amounts + index-values + lock dispatch) against the given library
// version, plus any extra validateOpt. Equivalent to
// OutputFromBytes(data, WithFullValidationAt(lib), validateOpt...).
//
// Use plain OutputFromBytes when you don't need lock dispatch tied to
// a specific library version — most callers eventually go through
// Output.Lock() (which uses the latest library) on demand.
func OutputFromBytesWithLib(data []byte, lib *Library, validateOpt ...func(*Output) error) (*Output, error) {
	opts := append([]func(*Output) error{WithFullValidationAt(lib)}, validateOpt...)
	return OutputFromBytes(data, opts...)
}

// OutputFromBytesMain is a backward-compat shim that returns the
// parsed Output along with its amounts and lock (decoded with the
// latest library). New code should call OutputFromBytes (with
// WithFullValidation if needed) and use Output.Amounts / Output.Lock
// on demand.
//
// Deprecated.
func OutputFromBytesMain(data []byte) (*Output, Amounts, Lock, error) {
	return OutputFromBytesMainWithLib(data, L(base.MaxSlot))
}

// OutputFromBytesMainWithLib is a backward-compat shim. New code
// should call OutputFromBytesWithLib (or plain OutputFromBytes with
// the validation hooks it actually needs) and unpack via methods on
// Output.
//
// Deprecated.
func OutputFromBytesMainWithLib(data []byte, lib *Library) (*Output, Amounts, Lock, error) {
	o, err := OutputFromBytesWithLib(data, lib)
	if err != nil {
		return nil, Amounts{}, nil, err
	}
	amountsBin, _ := o.At(int(ConstraintIndexAmounts))
	amounts, err := AmountsFromBytes(amountsBin)
	if err != nil {
		return nil, Amounts{}, nil, err
	}
	ivBin, _ := o.At(int(ConstraintIndexIndexValues))
	lockBin, _ := o.At(int(ConstraintIndexLock))
	lock, err := LockFromOutputElementsWithLib(ivBin, lockBin, lib)
	if err != nil {
		return nil, Amounts{}, nil, err
	}
	return o, amounts, lock, nil
}

// WithAmountsParsed validates that element 0 decodes as a well-formed
// amounts vector (each element is a uint64). Library-free.
func WithAmountsParsed() func(*Output) error {
	return func(o *Output) error {
		bin, err := o.At(int(ConstraintIndexAmounts))
		if err != nil {
			return fmt.Errorf("WithAmountsParsed: %w", err)
		}
		if _, err = AmountsFromBytes(bin); err != nil {
			return fmt.Errorf("WithAmountsParsed: %w", err)
		}
		return nil
	}
}

// WithIndexValuesParsed validates that element 1 decodes as a well-
// formed index-value tuple (or is empty). Library-free.
func WithIndexValuesParsed() func(*Output) error {
	return func(o *Output) error {
		bin, err := o.At(int(ConstraintIndexIndexValues))
		if err != nil {
			return fmt.Errorf("WithIndexValuesParsed: %w", err)
		}
		if _, err = IndexValuesFromBytes(bin); err != nil {
			return fmt.Errorf("WithIndexValuesParsed: %w", err)
		}
		return nil
	}
}

// WithLockParsed validates that element 2 dispatches to a known lock
// kind using the latest library. Library-dependent.
func WithLockParsed() func(*Output) error {
	return WithLockParsedAt(L(base.MaxSlot))
}

// WithLockParsedAt is the explicit-library variant of WithLockParsed.
func WithLockParsedAt(lib *Library) func(*Output) error {
	return func(o *Output) error {
		ivBin, err := o.At(int(ConstraintIndexIndexValues))
		if err != nil {
			return fmt.Errorf("WithLockParsedAt: %w", err)
		}
		lockBin, err := o.At(int(ConstraintIndexLock))
		if err != nil {
			return fmt.Errorf("WithLockParsedAt: %w", err)
		}
		if _, err = LockFromOutputElementsWithLib(ivBin, lockBin, lib); err != nil {
			return fmt.Errorf("WithLockParsedAt: %w", err)
		}
		return nil
	}
}

// WithFullValidation runs amounts + index-values + lock validation
// using the latest library.
func WithFullValidation() func(*Output) error {
	return WithFullValidationAt(L(base.MaxSlot))
}

// WithFullValidationAt is the explicit-library variant of
// WithFullValidation.
func WithFullValidationAt(lib *Library) func(*Output) error {
	amounts := WithAmountsParsed()
	indexValues := WithIndexValuesParsed()
	lock := WithLockParsedAt(lib)
	return func(o *Output) error {
		if err := amounts(o); err != nil {
			return err
		}
		if err := indexValues(o); err != nil {
			return err
		}
		return lock(o)
	}
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
	ret := make([][]byte, o.NumElements())
	o.ForEach(func(i int, data []byte) bool {
		ret[i] = data
		return true
	})
	return ret
}

// IndexValues returns the parsed index-value tuple at output element
// index 1. Each non-empty entry produces one trie index entry under
// TriePartitionControllers; empty entries are skipped.
func (o *Output) IndexValues() [][]byte {
	bin, err := o.At(int(ConstraintIndexIndexValues))
	util.AssertNoError(err)
	values, err := IndexValuesFromBytes(bin)
	util.AssertNoError(err)
	return values
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

// WithLock writes the lock onto the output: index-value tuple at output
// element index 1 (from lock.IndexValues) and lock bytecode at output
// element index 2 (from lock.LockBytecode).
func (o *OutputBuilder) WithLock(lock Lock) *OutputBuilder {
	o.PutConstraint(IndexValuesTupleBytes(lock.IndexValues()), ConstraintIndexIndexValues)
	o.PutConstraint(lock.LockBytecode(), ConstraintIndexLock)
	return o
}

// IndexValuesTupleBytes serialises a list of index values into the wire form of
// the index-value tuple stored at output slot 1. Empty input → empty bytes
// (no tuple), which is parsed as "this UTXO is not indexed".
func IndexValuesTupleBytes(values [][]byte) []byte {
	if len(values) == 0 {
		return nil
	}
	t := tuples.EmptyTupleEditable(256)
	for _, v := range values {
		t.MustPush(v)
	}
	return t.Tuple().Bytes()
}

// IndexValuesFromBytes parses a serialised index-value tuple back to its
// element slice. Empty bytes → empty slice (no entries).
func IndexValuesFromBytes(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	t, err := tuples.TupleFromBytes(data, 256)
	if err != nil {
		return nil, err
	}
	ret := make([][]byte, 0, t.NumElements())
	t.ForEach(func(_ int, v []byte) bool {
		ret = append(ret, v)
		return true
	})
	return ret, nil
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
	util.Assertf(o.NumElements() < 256, "too many UTXO elements")
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

// PutLock writes the lock onto the output: index-value tuple at output
// element index 1 and lock bytecode at output element index 2.
func (o *OutputBuilder) PutLock(lock Lock) {
	o.PutConstraint(IndexValuesTupleBytes(lock.IndexValues()), ConstraintIndexIndexValues)
	o.PutConstraint(lock.LockBytecode(), ConstraintIndexLock)
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

// Lock reconstructs the Lock from the output's index-value tuple
// (element index 1) and lock bytecode (element index 2).
func (o *Output) Lock() Lock {
	ret, err := LockFromOutputElements(
		o.MustAt(int(ConstraintIndexIndexValues)),
		o.MustAt(int(ConstraintIndexLock)),
	)
	util.AssertNoError(err)
	return ret
}

// TimeLock returns the timelock slot if the output has a timelock constraint.
func (o *Output) TimeLock() (uint32, bool) {
	var ret Timelock
	var err error
	lib := L(base.MaxSlot)
	idx := o.IndexFunc(func(i int, data []byte) bool {
		if byte(i) < ConstraintIndexChain {
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

// ChainConstraint parses the chain constraint at fixed index 2. Returns nil if not found.
func (o *Output) ChainConstraint() *ChainConstraint {
	if o.NumElements() <= int(ConstraintIndexChain) {
		return nil
	}
	data, err := o.At(int(ConstraintIndexChain))
	if err != nil {
		return nil
	}
	ret, err := ChainConstraintFromBytesWithLib(data, L(base.MaxSlot))
	if err != nil {
		return nil
	}
	return ret
}

// SequencerConstraint finds and parses the sequencer constraint. Returns 0xff as index if not found.
func (o *Output) SequencerConstraint() (*SequencerConstraint, byte) {
	var ret *SequencerConstraint
	var err error
	lib := L(base.MaxSlot)
	idx := o.IndexFunc(func(idx int, data []byte) bool {
		if byte(idx) < ConstraintIndexChain {
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
	chainConstraint := o.ChainConstraint()
	if chainConstraint == nil {
		return nil, false
	}
	var err error
	var seqConstraint *SequencerConstraint
	lib := L(base.MaxSlot)
	idx := o.IndexFunc(func(i int, data []byte) bool {
		if byte(i) <= ConstraintIndexChain {
			return false
		}
		seqConstraint, err = SequencerConstraintFromBytesWithLib(data, lib)
		return err == nil
	})
	if idx < 0 {
		return nil, false
	}

	var pSeqData *seqdata.SequencerData
	if seqData, err := ParseSequencerData(o); err == nil {
		pSeqData = &seqData
	}

	return &SequencerOutputData{
		SequencerConstraint: seqConstraint,
		ChainConstraint:     chainConstraint,
		AmountOnChain:       o.TokenBalance(),
		SequencerData:       pSeqData,
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
		if byte(i) < ConstraintIndexChain {
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
		if i == int(ConstraintIndexAmounts) {
			// amounts
			if a, err := AmountsFromBytes(data); err != nil {
				ret.Add("%s%d: amounts = '%v'", prefix, i, err)
			} else {
				ret.Add("%s%d: amounts = %s", prefix, i, a.String())
			}
			return true
		}
		if i == int(ConstraintIndexIndexValues) {
			// index-value tuple — pure data, not a constraint
			values, err := IndexValuesFromBytes(data)
			if err != nil || len(values) == 0 {
				ret.Add("%s%d: index values: <empty>", prefix, i)
			} else {
				parts := make([]string, len(values))
				for j, v := range values {
					if len(v) == 0 {
						parts[j] = "<empty>"
					} else {
						parts[j] = "0x" + hex.EncodeToString(v)
					}
				}
				ret.Add("%s%d: index values: [%s]", prefix, i, strings.Join(parts, ", "))
			}
			return true
		}
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

// LinesPlainSource formats UTXO elements as EasyFL source, with amounts shown
// as a parsed vector and the index-values tuple decoded as a list.
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
		if byte(i) == ConstraintIndexIndexValues {
			values, err := IndexValuesFromBytes(data)
			if err != nil || len(values) == 0 {
				ret.Add("index values: <empty>")
			} else {
				parts := make([]string, len(values))
				for j, v := range values {
					if len(v) == 0 {
						parts[j] = "<empty>"
					} else {
						parts[j] = "0x" + hex.EncodeToString(v)
					}
				}
				ret.Add("index values: [" + strings.Join(parts, ", ") + "]")
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

// LinesPlainHR formats UTXO elements as human-readable strings, with amounts shown
// as a parsed vector and the index-values tuple shown as a placeholder.
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
		if byte(i) == ConstraintIndexIndexValues {
			values, err := IndexValuesFromBytes(data)
			if err != nil || len(values) == 0 {
				ret.Add("index values: <empty>")
			} else {
				parts := make([]string, len(values))
				for j, v := range values {
					if len(v) == 0 {
						parts[j] = "<empty>"
					} else {
						parts[j] = "0x" + hex.EncodeToString(v)
					}
				}
				ret.Add("index values: [" + strings.Join(parts, ", ") + "]")
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

// ParseAsChainOutput parses raw output data as a chain output. For origin
// outputs whose serialised ChainID is NilChainID, the returned ChainID is
// resolved as blake2b(outputID) so callers see the same value the chain
// constraint enforces post-origin.
func (o *OutputDataWithID) ParseAsChainOutput() (*OutputWithChainID, error) {
	var chainConstr *ChainConstraint

	ret, err := o.Parse(func(oParsed *Output) error {
		chainConstr = oParsed.ChainConstraint()
		if chainConstr == nil {
			return fmt.Errorf("can't find chain constraint")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	resolved := *chainConstr
	if resolved.ChainID == base.NilChainID {
		resolved.ChainID = blake2b.Sum256(o.ID[:])
	}
	return &OutputWithChainID{
		OutputWithID: *ret,
		ChainConstraintData: ChainConstraintData{
			ChainConstraint: resolved,
		},
	}, nil
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
	cc := o.ChainConstraint()
	if cc == nil {
		return ChainConstraintData{}, false
	}
	ret := ChainConstraintData{
		ChainConstraint: *cc,
	}
	if cc.IsOrigin() {
		ret.ChainID = blake2b.Sum256(oid[:])
	}
	return ret, true
}

// ExtractChainID returns the ChainID and whether a chain constraint exists.
func (o *OutputWithID) ExtractChainID() (chainID base.ChainID, ok bool) {
	ret, ok := ExtractChainData(o.Output, o.ID)
	return ret.ChainID, ok
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
	if cc := o.Output.ChainConstraint(); cc != nil {
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
	if cc := o.Output.ChainConstraint(); cc != nil {
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

// MustValidOutput panics if the output's index-value tuple at slot 1 or
// lock bytecode at slot 2 are structurally malformed. Does not require
// the lock kind to be registered as a known Go type — arbitrary EasyFL
// bytecode is admissible at slot 2 (claude/utxo-indexing.md §4).
func (o *Output) MustValidOutput() {
	_, err := IndexValuesFromBytes(o.MustConstraintAt(ConstraintIndexIndexValues))
	util.AssertNoError(err)
	_, err = L(base.MaxSlot).ParsePrefixBytecode(o.MustConstraintAt(ConstraintIndexLock))
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
		cc := o.Output.ChainConstraint()
		if cc == nil {
			continue
		}
		d := &OutputWithChainID{
			OutputWithID: OutputWithID{
				ID:     o.ID,
				Output: o.Output,
			},
			ChainConstraintData: ChainConstraintData{
				ChainConstraint: *cc,
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
		ch := o.ChainConstraint()
		if ch == nil {
			return true
		}
		d := &OutputWithChainID{
			OutputWithID: OutputWithID{
				ID:     odata.ID,
				Output: o,
			},
			ChainConstraintData: ChainConstraintData{
				ChainConstraint: *ch,
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

// SeqMilestoneDataFixedIndex is the fixed tuple index of the sequencer
// milestone data on a sequencer output. Output layout:
// [0] amounts, [1] index-value tuple, [2] lock, [3] chain, [4] sequencer
// constraint, [5] sequencer milestone data.
const SeqMilestoneDataFixedIndex = 5

// ParseSequencerData parses the sequencer data from constraint index 5.
func ParseSequencerData(o *Output) (ret seqdata.SequencerData, err error) {
	if o.NumElements() <= SeqMilestoneDataFixedIndex {
		err = fmt.Errorf("ParseSequencerData: wrong number of UTXO elements")
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
	lock := o.Lock()
	if t, ok := lock.(*TagAlongLock); ok {
		return t
	}
	return nil
}

// EnoughAmountForStorageDeposit returns an error if the token balance is below the minimum storage deposit.
func (o *Output) EnoughAmountForStorageDeposit() error {
	m := MinimumStorageDeposit(o)
	bal := o.TokenBalance()
	if bal >= m {
		return nil
	}
	return fmt.Errorf("storage deposit not met: balance %s, required %s (%s short, output size %d bytes)\n%s",
		util.Th(bal), util.Th(m), util.Th(m-bal), len(o.Bytes()), o.LinesHR("     ").String())
}
