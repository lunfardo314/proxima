package core

import (
	"fmt"
	"io"
	"strings"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazyslice"
	"github.com/lunfardo314/proxima/util/lines"
	"golang.org/x/crypto/blake2b"
)

type (
	Output struct {
		arr *lazyslice.Array
	}

	OutputWithID struct {
		ID     OutputID
		Output *Output
	}

	OutputDataWithID struct {
		ID         OutputID
		OutputData []byte
	}

	OutputDataWithChainID struct {
		OutputDataWithID
		ChainID ChainID
	}

	OutputWithChainID struct {
		OutputWithID
		ChainID                    ChainID
		PredecessorConstraintIndex byte
	}

	SequencerOutputData struct {
		SequencerConstraint      *SequencerConstraint
		ChainConstraint          *ChainConstraint
		AmountOnChain            uint64
		SequencerConstraintIndex byte
	}
)

func NewOutput(overrideReadOnly ...func(o *Output)) *Output {
	ret := &Output{
		arr: lazyslice.EmptyArray(256),
	}
	if len(overrideReadOnly) > 0 {
		overrideReadOnly[0](ret)
	}
	ret.arr.SetReadOnly(true)
	return ret
}

func OutputBasic(amount uint64, lock Lock) *Output {
	return NewOutput(func(o *Output) {
		o.WithLock(lock).WithAmount(amount)
	})
}

func OutputFromBytesReadOnly(data []byte) (*Output, error) {
	ret, _, _, err := OutputFromBytesMain(data)
	return ret, err
}

func OutputFromBytesMain(data []byte) (*Output, Amount, Lock, error) {
	ret := &Output{
		arr: lazyslice.ArrayFromBytesReadOnly(data, 256),
	}
	var amount Amount
	var lock Lock
	var err error
	if ret.arr.NumElements() < 2 {
		return nil, 0, nil, fmt.Errorf("at least 2 constraints expected")
	}
	if amount, err = AmountFromBytes(ret.arr.At(int(ConstraintIndexAmount))); err != nil {
		return nil, 0, nil, err
	}
	if lock, err = LockFromBytes(ret.arr.At(int(ConstraintIndexLock))); err != nil {
		return nil, 0, nil, err
	}
	return ret, amount, lock, nil
}

func (o *Output) StemLock() (*StemLock, bool) {
	ret, ok := o.Lock().(*StemLock)
	return ret, ok
}

// WithAmount can only be used inside r/o override closure
func (o *Output) WithAmount(amount uint64) *Output {
	o.arr.PutAtIdxGrow(ConstraintIndexAmount, NewAmount(amount).Bytes())
	return o
}

func (o *Output) Amount() uint64 {
	ret, err := AmountFromBytes(o.arr.At(int(ConstraintIndexAmount)))
	util.AssertNoError(err)
	return uint64(ret)
}

// WithLock can only be used inside r/o override closure
func (o *Output) WithLock(lock Lock) *Output {
	o.PutConstraint(lock.Bytes(), ConstraintIndexLock)
	return o
}

func (o *Output) AsArray() *lazyslice.Array {
	return o.arr
}

func (o *Output) Bytes() []byte {
	return o.arr.Bytes()
}

// Clone clones output and makes it read-only. Optional function overrideReadOnly gives a chance
// to modify the output before it is locked for modification
func (o *Output) Clone(overrideReadOnly ...func(o *Output)) *Output {
	ret, err := OutputFromBytesReadOnly(o.Bytes())
	util.AssertNoError(err)
	if len(overrideReadOnly) > 0 {
		ret.arr.SetReadOnly(false)
		overrideReadOnly[0](ret)
		ret.arr.SetReadOnly(true)
	}
	return ret
}

// PushConstraint can only be used inside r/o override closure
func (o *Output) PushConstraint(c []byte) (byte, error) {
	if o.NumConstraints() >= 256 {
		return 0, fmt.Errorf("too many constraints")
	}
	o.arr.Push(c)
	return byte(o.arr.NumElements() - 1), nil
}

// PutConstraint can only be used inside r/o override closure
func (o *Output) PutConstraint(c []byte, idx byte) {
	o.arr.PutAtIdxGrow(idx, c)
}

func (o *Output) PutAmount(amount uint64) {
	o.PutConstraint(NewAmount(amount).Bytes(), ConstraintIndexAmount)
}

func (o *Output) PutLock(lock Lock) {
	o.PutConstraint(lock.Bytes(), ConstraintIndexLock)
}

func (o *Output) ConstraintAt(idx byte) []byte {
	return o.arr.At(int(idx))
}

func (o *Output) NumConstraints() int {
	return o.arr.NumElements()
}

func (o *Output) ForEachConstraint(fun func(idx byte, constr []byte) bool) {
	o.arr.ForEach(func(i int, data []byte) bool {
		return fun(byte(i), data)
	})
}

func (o *Output) Lock() Lock {
	ret, err := LockFromBytes(o.arr.At(int(ConstraintIndexLock)))
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
		ret, err = TimelockFromBytes(constr)
		if err == nil {
			// TODO optimize parsing
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

// SenderED25519 return sender address and constraintIndex if found, otherwise nil, 0xff
func (o *Output) SenderED25519() (AddressED25519, byte) {
	var ret *SenderED25519
	var err error
	foundIdx := byte(0xff)
	o.ForEachConstraint(func(idx byte, constr []byte) bool {
		if idx < ConstraintIndexFirstOptionalConstraint {
			return true
		}
		ret, err = SenderED25519FromBytes(constr)
		if err == nil {
			foundIdx = idx
			return false
		}
		return true
	})
	if foundIdx != 0xff {
		return ret.Address, foundIdx
	}
	return nil, 0xff
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
	return &SequencerOutputData{
		SequencerConstraintIndex: seqConstraintIndex,
		SequencerConstraint:      seqConstraint,
		ChainConstraint:          chainConstraint,
		AmountOnChain:            o.Amount(),
	}, true
}

func (o *Output) ConstraintWithName(name string) (Constraint, byte, bool) {
	var ret Constraint
	var retIdx byte
	var found bool
	o.ForEachConstraint(func(idx byte, data []byte) bool {
		constr, err := FromBytes(data)
		if err != nil {
			return false
		}
		if constr.Name() == name {
			ret = constr
			retIdx = idx
			found = true
			return false
		}
		return true
	})
	return ret, retIdx, found
}

func (o *Output) ToString(prefix ...string) string {
	return o.ToLines(prefix...).String()
}

func (o *Output) ToLines(prefix ...string) *lines.Lines {
	ret := lines.New()
	pref := ""
	if len(prefix) > 0 {
		pref = prefix[0]
	}
	o.arr.ForEach(func(i int, data []byte) bool {
		c, err := FromBytes(data)
		if err != nil {
			ret.Add("%s%d: %v (%d bytes)", pref, i, err, len(data))
		} else {
			ret.Add("%s%d: %s (%d bytes)", pref, i, c.String(), len(data))
		}
		return true
	})
	return ret
}

func (o *Output) WriteHTML(w io.Writer) {
	_, _ = w.Write([]byte("<table>"))
	o.arr.ForEach(func(i int, data []byte) bool {
		_, _ = w.Write([]byte("<tr>"))
		c, err := FromBytes(data)
		if err != nil {
			_, _ = fmt.Fprintf(w, "<td>%d:</td> <td><pre>%v</pre></td> <td>(%d bytes)</td>\n", i, err, len(data))
		} else {
			_, _ = fmt.Fprintf(w, "<td>%d:</td> <td>%s</td> <td>(%d bytes)</td>\n", i, c.String(), len(data))
		}
		_, _ = w.Write([]byte("</tr>"))
		return true
	})
	_, _ = w.Write([]byte("</table>"))
}

func (o *Output) ToHTMLTableString() string {
	var buf strings.Builder
	o.WriteHTML(&buf)
	return buf.String()
}

func (o *OutputDataWithID) Parse() (*OutputWithID, error) {
	ret, err := OutputFromBytesReadOnly(o.OutputData)
	if err != nil {
		return nil, err
	}
	return &OutputWithID{
		ID:     o.ID,
		Output: ret,
	}, nil
}

func (o *OutputDataWithID) ParseAsChainOutput() (*OutputWithChainID, byte, error) {
	oWithID, err := o.Parse()
	if err != nil {
		return nil, 0, err
	}
	constr, idx := oWithID.Output.ChainConstraint()
	if idx == 0xff {
		return nil, 0, fmt.Errorf("can't find chain constraint")
	}
	chainID := constr.ID
	if chainID == NilChainID {
		chainID = blake2b.Sum256(oWithID.ID[:])
	}
	return &OutputWithChainID{
		OutputWithID:               *oWithID,
		ChainID:                    chainID,
		PredecessorConstraintIndex: constr.PredecessorInputIndex,
	}, idx, nil
}

func (o *OutputDataWithID) MustParse() *OutputWithID {
	ret, err := o.Parse()
	util.AssertNoError(err)
	return ret
}

func (o *OutputWithID) ExtractChainID() (ChainID, bool) {
	cc, blockIdx := o.Output.ChainConstraint()
	if blockIdx == 0xff {
		return ChainID{}, false
	}
	ret := cc.ID
	if cc.ID == NilChainID {
		ret = blake2b.Sum256(o.ID[:])
	}
	return ret, true
}

func (o *OutputWithID) Timestamp() LogicalTime {
	return o.ID.Timestamp()
}

func (o *OutputWithID) Clone() *OutputWithID {
	return &OutputWithID{
		ID:     o.ID,
		Output: o.Output.Clone(),
	}
}

func (o *OutputWithID) String() string {
	return fmt.Sprintf(" ID: %s\nHex: %s\n%s", o.ID.String(), o.ID.StringHex(), o.Output.ToString("     "))
}

func (o *OutputWithID) Short() string {
	return fmt.Sprintf("%s\n%s", o.ID.Short(), o.Output.ToString("   "))
}

func (o *OutputWithID) IDShort() string {
	return o.ID.Short()
}

func OutputsWithIdToString(outs ...*OutputWithID) string {
	ret := lines.New()
	for i, o := range outs {
		ret.Add("%d : %s", i, o.ID.Short()).
			Append(o.Output.ToLines("      "))
	}
	return ret.String()
}
