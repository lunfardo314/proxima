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
	Output struct {
		*tuples.Tuple
	}

	OutputBuilder struct {
		*tuples.TupleEditable
	}

	OutputWithID struct {
		ID     base.OutputID
		Output *Output
	}

	OutputDataWithID struct {
		ID   base.OutputID
		Data []byte
	}

	OutputDataWithChainID struct {
		OutputDataWithID
		ChainID base.ChainID
	}

	OutputWithChainID struct {
		OutputWithID
		ChainConstraintData
	}

	ChainConstraintData struct {
		ChainConstraint
		ChainConstraintIndex byte
	}

	SequencerOutputData struct {
		SequencerConstraint      *SequencerConstraint
		ChainConstraint          *ChainConstraint
		AmountOnChain            uint64
		SequencerConstraintIndex byte
		SequencerData            *seqdata.SequencerData
	}
)

func NewOutput(buildFun func(o *OutputBuilder)) *Output {
	arr := tuples.EmptyTupleEditable(256)
	builder := &OutputBuilder{arr}
	buildFun(builder)
	return &Output{arr.Tuple()}
}

func OutputBasic(amount int64, lock Lock) *Output {
	return NewOutput(func(o *OutputBuilder) {
		o.WithAmounts(amount).WithLock(lock)
	})
}

func OutputBuilderFromBytes(data []byte) (*OutputBuilder, error) {
	ret, err := tuples.TupleFromBytesEditable(data, 256)
	if err != nil {
		return nil, fmt.Errorf("OutputBuilderFromBytes: %v", err)
	}
	return &OutputBuilder{ret}, nil
}

func OutputFromBytes(data []byte, validateOpt ...func(*Output) error) (*Output, error) {
	ret, _, _, err := OutputFromBytesMain(data)
	if err != nil {
		return nil, err
	}
	for _, validate := range validateOpt {
		if err := validate(ret); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func OutputFromHexString(hexStr string, validateOpt ...func(*Output) error) (*Output, error) {
	data, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, err
	}
	ret, _, _, err := OutputFromBytesMain(data)
	if err != nil {
		return nil, err
	}
	for _, validate := range validateOpt {
		if err := validate(ret); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func OutputFromBytesMain(data []byte) (*Output, Amounts, Lock, error) {
	arr, err := tuples.TupleFromBytes(bytes.Clone(data), 256)
	if err != nil {
		return nil, nil, nil, err
	}
	ret := &Output{arr}

	var amount Amounts
	var lock Lock
	if ret.NumElements() < 2 {
		return nil, nil, nil, fmt.Errorf("at least 2 constraints expected")
	}
	amountBin, err := ret.At(int(ConstraintIndexAmounts))
	if err != nil {
		return nil, nil, nil, err
	}
	if amount, err = AmountsFromBytes(amountBin); err != nil {
		return nil, nil, nil, err
	}
	lockBin, err := ret.At(int(ConstraintIndexLock))
	if err != nil {
		return nil, nil, nil, err
	}
	if lock, err = LockFromBytes(lockBin); err != nil {
		return nil, nil, nil, err
	}
	return ret, amount, lock, nil
}

func (o *Output) ConstraintsRawBytes() [][]byte {
	ret := make([][]byte, o.NumConstraints())
	o.ForEach(func(i int, data []byte) bool {
		ret[i] = data
		return true
	})
	return ret
}

func (o *Output) StemLock() (*StemLock, bool) {
	ret, ok := o.Lock().(*StemLock)
	return ret, ok
}

func (o *Output) MustStemLock() *StemLock {
	ret, ok := o.StemLock()
	util.Assertf(ok, "can't get stem output")
	return ret
}

// WithAmounts can only be used inside r/o override closure
func (o *OutputBuilder) WithAmounts(amount ...int64) *OutputBuilder {
	o.MustPutAtIdxWithPadding(ConstraintIndexAmounts, NewAmounts(amount...).Bytes())
	return o
}

func (o *OutputBuilder) WithTokenBalance(bal uint64) *OutputBuilder {
	return o.WithAmounts(int64(bal))
}

func (o *Output) Amounts() Amounts {
	bin, err := o.At(int(ConstraintIndexAmounts))
	util.AssertNoError(err)
	ret, err := AmountsFromBytes(bin)
	util.AssertNoError(err)
	return ret
}

func (o *Output) TokenBalance() uint64 {
	bin, err := o.At(int(ConstraintIndexAmounts))
	util.AssertNoError(err)
	ret, err := TokenBalanceFromAmountsBytes(bin)
	util.AssertNoError(err)
	return uint64(ret)
}

func (o *Output) FrozenCoverage(i byte) int64 {
	return o.Amounts().FrozenCoverageAt(i)
}

func (o *Output) InflatableAmount() uint64 {
	return o.TokenBalance() + uint64(o.FrozenCoverage(0))
}

func (o *OutputWithChainID) AdjustedFrozenCoverage(txTs base.LedgerTime) int64 {
	predTs := o.ID.Timestamp()
	util.Assertf(txTs.AfterOrEqual(predTs), "txTs.AfterOrEqual(predTs)")
	diff := Const.DiffEpochs(o.ChainID, txTs, o.ID.Timestamp())
	if diff >= int(Const.MaxFrozenEpochs) {
		return 0
	}
	return o.Output.FrozenCoverage(byte(diff))
}

// WithLock can only be used inside r/o override closure
func (o *OutputBuilder) WithLock(lock Lock) *OutputBuilder {
	o.PutConstraint(lock.Bytes(), ConstraintIndexLock)
	return o
}

func (o *Output) Hex() string {
	return hex.EncodeToString(o.Bytes())
}

// Clone clones output and gives a chance to modify it
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

// MustPushConstraint can only be used inside the edit closure
func (o *OutputBuilder) MustPushConstraint(c []byte) byte {
	util.Assertf(o.NumConstraints() < 256, "too many constraints")
	o.MustPush(c)
	return byte(o.NumElements() - 1)
}

// PutConstraint places bytecode at the specific index
func (o *OutputBuilder) PutConstraint(c []byte, idx byte) {
	o.MustPutAtIdxWithPadding(idx, c)
}

func (o *OutputBuilder) PutAmounts(amount ...int64) {
	o.PutConstraint(NewAmounts(amount...).Bytes(), ConstraintIndexAmounts)
}

func (o *OutputBuilder) PutLock(lock Lock) {
	o.PutConstraint(lock.Bytes(), ConstraintIndexLock)
}

func (o *Output) MustConstraintAt(idx byte) []byte {
	return o.MustAt(int(idx))
}

func (o *Output) ConstraintAt(idx byte) ([]byte, error) {
	return o.At(int(idx))
}

func (o *OutputBuilder) NumConstraints() int {
	return o.NumElements()
}

func (o *Output) NumConstraints() int {
	return o.NumElements()
}

func (o *Output) ForEachConstraint(fun func(idx byte, constr []byte) bool) {
	o.ForEach(func(i int, data []byte) bool {
		return fun(byte(i), data)
	})
}

func (o *Output) Lock() Lock {
	ret, err := LockFromBytes(o.MustAt(int(ConstraintIndexLock)))
	util.AssertNoError(err)
	return ret
}

func (o *Output) AccountIDs() []AccountID {
	ret := make([]AccountID, 0)
	for _, a := range o.Lock().Accounts() {
		ret = append(ret, a.AccountID())
	}
	return ret
}

func (o *Output) TimeLock() (uint32, bool) {
	var ret Timelock
	var err error
	found := false
	o.ForEachConstraint(func(idx byte, constr []byte) bool {
		if idx < ConstraintIndexFirstOptionalConstraint {
			return true
		}
		if ret, err = TimelockFromBytes(constr); err == nil {
			found = true
			return false
		}
		return true
	})
	if found {
		return uint32(ret), true
	}
	return 0, false
}

// ChainConstraint finds and parses chain constraint. Returns its constraintIndex or 0xff if not found
func (o *Output) ChainConstraint() (*ChainConstraint, byte) {
	var ret *ChainConstraint
	var err error
	found := byte(0xff)
	o.ForEachConstraint(func(idx byte, constr []byte) bool {
		if idx < ConstraintIndexFirstOptionalConstraint {
			return true
		}
		ret, err = ChainConstraintFromBytes(constr)
		if err == nil {
			found = idx
			return false
		}
		return true
	})
	if found != 0xff {
		return ret, found
	}
	return nil, 0xff
}

// SequencerConstraint finds and parses chain constraint. Returns its constraintIndex or 0xff if not found
func (o *Output) SequencerConstraint() (*SequencerConstraint, byte) {
	var ret *SequencerConstraint
	var err error
	found := byte(0xff)
	o.ForEachConstraint(func(idx byte, constr []byte) bool {
		if idx < ConstraintIndexFirstOptionalConstraint {
			return true
		}
		ret, err = SequencerConstraintFromBytes(constr)
		if err == nil {
			found = idx
			return false
		}
		return true
	})
	if found != 0xff {
		return ret, found
	}
	return nil, 0xff
}

// IsSequencerOutput output contains sequencer constraint
func (o *Output) IsSequencerOutput() bool {
	_, idx := o.SequencerConstraint()
	return idx != 0xff
}

func (o *Output) Inflation() uint64 {
	return o.Amounts().InflationAmount()
}

func (o *Output) SequencerOutputData() (*SequencerOutputData, bool) {
	chainConstraint, chainConstraintIndex := o.ChainConstraint()
	if chainConstraintIndex == 0xff {
		return nil, false
	}
	var err error
	seqConstraintIndex := byte(0xff)
	var seqConstraint *SequencerConstraint

	o.ForEachConstraint(func(idx byte, constr []byte) bool {
		if idx < ConstraintIndexFirstOptionalConstraint || idx == chainConstraintIndex {
			return true
		}
		seqConstraint, err = SequencerConstraintFromBytes(constr)
		if err == nil {
			seqConstraintIndex = idx
			return false
		}
		return true
	})
	if seqConstraintIndex == 0xff {
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

func (o *Output) DelegationLock() *DelegateLock {
	lock := o.Lock()
	if lock.Name() != DelegateLockName {
		return nil
	}
	return lock.(*DelegateLock)
}

func (o *Output) EnsureStopDelegationConstraint() (*EnsureStopDelegation, byte) {
	var ret *EnsureStopDelegation
	var err error
	found := byte(0xff)
	o.ForEachConstraint(func(idx byte, constr []byte) bool {
		if idx < ConstraintIndexFirstOptionalConstraint {
			return true
		}
		ret, err = EnsureStopDelegationFromBytes(constr)
		if err == nil {
			found = idx
			return false
		}
		return true
	})
	if found != 0xff {
		return ret, found
	}
	return nil, 0xff
}

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

func (o *Output) _lines(prefix string, source bool, verbose bool) *lines.Lines {
	ret := lines.New()
	o.ForEachConstraint(func(i byte, data []byte) bool {
		bc := ""
		if verbose {
			bc = fmt.Sprintf(prefix+"   bytecode: %s", easyfl_util.Fmt(data))
		}
		c, err := ConstraintFromBytes(data)

		if err != nil {
			if src, err := L().DecompileBytecode(data); err != nil {
				ret.Add("%s%d: bytecode=%s (%v)", hex.EncodeToString(data), err)
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

func (o *Output) LinesPlainSource() *lines.Lines {
	ret := lines.New()
	o.ForEachConstraint(func(i byte, data []byte) bool {
		c, err := ConstraintFromBytes(data)
		if err != nil {
			ret.Add(err.Error())
		} else {
			ret.Add(c.Source())
		}
		return true
	})
	return ret
}

func (o *Output) LinesPlainHR() *lines.Lines {
	ret := lines.New()
	o.ForEachConstraint(func(i byte, data []byte) bool {
		c, err := ConstraintFromBytes(data)
		if err != nil {
			ret.Add(err.Error())
		} else {
			ret.Add(c.String())
		}
		return true
	})
	return ret
}

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

// ParseAsChainOutput parses raw output data expecting chain output. Returns parsed output and index of the chain constraint in it
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

func (o *OutputDataWithID) MustParse() *OutputWithID {
	ret, err := o.Parse()
	util.AssertNoError(err)
	return ret
}

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

// ExtractChainID return chainID, predecessor constraint index, existence flag
func (o *OutputWithID) ExtractChainID() (chainID base.ChainID, predecessorConstraintIndex byte, ok bool) {
	ret, ok := ExtractChainData(o.Output, o.ID)
	return ret.ChainID, ret.PredecessorInputIndex, ok
}

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

func OutputsWithIDToString(outs ...*OutputWithID) string {
	ret := lines.New()
	for i, o := range outs {
		ret.Add("%d : %s", i, o.ID.StringShort()).
			Add("      bytecode: %s", o.Output.Hex()).
			Append(o.Output.Lines("      "))
	}
	return ret.String()
}

func (o *Output) hasConstraintAt(pos byte, constraintName string) bool {
	constr, err := ConstraintFromBytes(o.MustConstraintAt(pos))
	util.AssertNoError(err)

	return constr.Name() == constraintName
}

func (o *Output) MustHaveConstraintAnyOfAt(pos byte, names ...string) {
	util.Assertf(o.NumConstraints() >= int(pos), "no constraint at position %d", pos)

	constr, err := ConstraintFromBytes(o.MustConstraintAt(pos))
	util.AssertNoError(err)

	for _, n := range names {
		if constr.Name() == n {
			return
		}
	}
	util.Panicf("any of %+v was expected at the position %d, got '%s' instead", names, pos, constr.Name())
}

// MustValidOutput checks if amount and lock constraints are as expected
func (o *Output) MustValidOutput() {
	o.MustHaveConstraintAnyOfAt(0, AmountsConstraintName)
	_, err := LockFromBytes(o.MustConstraintAt(1))
	util.AssertNoError(err)
}

// HashOutputs calculates input commitment from outputs: the hash of lazyarray composed of output data
func HashOutputs(outs ...*Output) [32]byte {
	arr := tuples.EmptyTupleEditable(256)
	for _, o := range outs {
		arr.MustPush(o.Bytes())
	}
	return blake2b.Sum256(arr.Bytes())
}

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

func ParseOutputDataAndFilter(outs []*OutputDataWithID, filter func(oid *base.OutputID, o *Output) bool) ([]*OutputWithID, error) {
	ret := make([]*OutputWithID, 0, len(outs))
	for _, od := range outs {
		out, err := OutputFromBytes(od.Data)
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

func forEachOutputReadOnly(outs []*OutputDataWithID, fun func(o *Output, odata *OutputDataWithID) bool) error {
	for _, odata := range outs {
		o, err := OutputFromBytes(odata.Data)
		if err != nil {
			return err
		}
		if !fun(o, odata) {
			return nil
		}
	}
	return nil
}

func ParseChainConstraintsFromData(outs []*OutputDataWithID) ([]*OutputWithChainID, error) {
	ret := make([]*OutputWithChainID, 0)
	err := forEachOutputReadOnly(outs, func(o *Output, odata *OutputDataWithID) bool {
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

// ParseSequencerData expected at index 4
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

// TagAlongLock return tag-along lock if present, otherwise nil
func (o *Output) TagAlongLock() *TagAlongLock {
	ret, err := TagAlongLockFromBytes(o.MustAt(int(ConstraintIndexLock)))
	if err != nil {
		return nil
	}
	return ret
}

func (o *Output) EnoughAmountForStorageDeposit() error {
	m := MinimumStorageDeposit(o)
	if o.TokenBalance() >= m {
		return nil
	}
	return fmt.Errorf("not enough token balance (%s) for the minimum storage deposit (%s) in the output (size %d bytes):\n%s",
		util.Th(o.TokenBalance()), util.Th(m), len(o.Bytes()), o.LinesHR("     ").String())
}
