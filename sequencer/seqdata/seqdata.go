package seqdata

import (
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/lines"
)

type SequencerData struct {
	base.SmallPersistentMap
}

const (
	KeyName = byte(iota)
	KeyMinimumFee
	KeyInflationMarginPromille
	KeyChainHeight
	KeyBranchHeight
	KeyPace
)

func New() SequencerData {
	return SequencerData{base.NewSmallPersistentMap()}
}

func (sd *SequencerData) Name() string {
	return string(sd.Get(KeyName))
}

func (sd *SequencerData) SetName(name string) {
	sd.Set(KeyName, []byte(name))
}

func (sd *SequencerData) MinimumFee() (ret uint64) {
	ret, _ = easyfl_util.Uint64FromBytes(sd.Get(KeyMinimumFee))
	return
}

func (sd *SequencerData) SetMinimumFee(fee uint64) {
	sd.Set(KeyMinimumFee, easyfl_util.TrimmedLeadingZeroUint64(fee))
}

func (sd *SequencerData) ChainHeight() (ret uint32) {
	ret, _ = easyfl_util.Uint32FromBytes(sd.Get(KeyChainHeight))
	return
}

func (sd *SequencerData) IncChainHeight() {
	sd.Set(KeyChainHeight, easyfl_util.TrimmedLeadingZeroUint32(sd.ChainHeight()+1))
}

func (sd *SequencerData) BranchHeight() (ret uint32) {
	ret, _ = easyfl_util.Uint32FromBytes(sd.Get(KeyBranchHeight))
	return
}

func (sd *SequencerData) IncBranchHeight() {
	sd.Set(KeyBranchHeight, easyfl_util.TrimmedLeadingZeroUint32(sd.BranchHeight()+1))
}

func (sd *SequencerData) InflationMarginPromille() (ret uint16) {
	ret, _ = easyfl_util.Uint16FromBytes(sd.Get(KeyInflationMarginPromille))
	return
}

func (sd *SequencerData) SetInflationMarginPromille(margin uint16) {
	sd.Set(KeyInflationMarginPromille, easyfl_util.TrimmedLeadingZeroUint16(margin))
}

func (sd *SequencerData) Pace() (ret byte) {
	ret, _ = easyfl_util.ByteFromBytes(sd.Get(KeyPace))
	return
}

func (sd *SequencerData) SetPace(pace byte) {
	if pace == 0 {
		sd.Set(KeyPace, nil)
	} else {
		sd.Set(KeyPace, []byte{pace})
	}
}

func SequencerDataFromBytes(data []byte) (ret SequencerData, err error) {
	ret.SmallPersistentMap, err = base.SmallPersistentMapFromBytes(data)
	return
}

func (sd *SequencerData) Lines(prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)
	ln.Add("Name: %s", sd.Name())
	ln.Add("Minimum fee: %d", sd.MinimumFee())
	ln.Add("Pace: %d", sd.Pace())
	ln.Add("Inflation margin promille: %d", sd.InflationMarginPromille())
	ln.Add("Chain height: %d", sd.ChainHeight())
	ln.Add("Branch height: %d", sd.BranchHeight())
	return ln
}
