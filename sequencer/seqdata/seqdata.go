package seqdata

import (
	"math"

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
	KeyInflationProfitMarginPromille
	KeyGenerous
	KeyChainHeight
	KeyBranchHeight
	KeyPace
)

func New() *SequencerData {
	return &SequencerData{base.NewSmallPersistentMap()}
}

func (sd *SequencerData) Clone(modify ...func(sdUpdated *SequencerData)) *SequencerData {
	ret := &SequencerData{sd.SmallPersistentMap.Clone()}
	if len(modify) > 0 {
		modify[0](ret)
	}
	return ret
}

func (sd *SequencerData) Name() string {
	return string(sd.Get(KeyName))
}

func (sd *SequencerData) SetName(name string) *SequencerData {
	sd.Set(KeyName, []byte(name))
	return sd
}

func (sd *SequencerData) MinimumFee() (ret uint64) {
	ret, _ = easyfl_util.Uint64FromBytes(sd.Get(KeyMinimumFee))
	return
}

func (sd *SequencerData) SetMinimumFee(fee uint64) *SequencerData {
	sd.Set(KeyMinimumFee, easyfl_util.TrimmedLeadingZeroUint64(fee))
	return sd
}

func (sd *SequencerData) ChainHeight() (ret uint32) {
	ret, _ = easyfl_util.Uint32FromBytes(sd.Get(KeyChainHeight))
	return
}

func (sd *SequencerData) IncChainHeight(add ...uint32) *SequencerData {
	s := uint32(1)
	if len(add) > 0 {
		s = add[0]
	}
	sd.Set(KeyChainHeight, easyfl_util.TrimmedLeadingZeroUint32(sd.ChainHeight()+s))
	return sd
}

func (sd *SequencerData) BranchHeight() (ret uint32) {
	ret, _ = easyfl_util.Uint32FromBytes(sd.Get(KeyBranchHeight))
	return
}

func (sd *SequencerData) IncBranchHeight(add ...uint32) *SequencerData {
	s := uint32(1)
	if len(add) > 0 {
		s = add[0]
	}
	sd.Set(KeyBranchHeight, easyfl_util.TrimmedLeadingZeroUint32(sd.BranchHeight()+s))
	return sd
}

func (sd *SequencerData) InflationProfitMarginPromille() (ret uint16) {
	ret, _ = easyfl_util.Uint16FromBytes(sd.Get(KeyInflationProfitMarginPromille))
	return
}

func (sd *SequencerData) InflationProfitMargin(amount uint64) (ret uint64) {
	p := sd.InflationProfitMarginPromille()
	if p == 0 {
		return 0
	}
	if p > 1000 {
		// everything is taken
		return amount
	}
	if amount > math.MaxUint64/uint64(p) {
		return 0
	}
	return (amount * uint64(p)) / 1000
}

func (sd *SequencerData) SetInflationMarginPromille(margin uint16) *SequencerData {
	sd.Set(KeyInflationProfitMarginPromille, easyfl_util.TrimmedLeadingZeroUint16(margin))
	return sd
}

func (sd *SequencerData) Pace() (ret byte) {
	ret, _ = easyfl_util.ByteFromBytes(sd.Get(KeyPace))
	return
}

func (sd *SequencerData) SetPace(pace byte) *SequencerData {
	if pace == 0 {
		sd.Set(KeyPace, nil)
	} else {
		sd.Set(KeyPace, []byte{pace})
	}
	return sd
}

func (sd *SequencerData) SetGenerous(generous bool) *SequencerData {
	if generous {
		sd.Set(KeyGenerous, []byte{0xff})
	} else {
		sd.Set(KeyGenerous, nil)
	}
	return sd
}

func (sd *SequencerData) IsGenerous() bool {
	return len(sd.Get(KeyGenerous)) > 0
}

func FromBytes(data []byte) (ret SequencerData, err error) {
	ret.SmallPersistentMap, err = base.SmallPersistentMapFromBytes(data)
	return
}

func (sd *SequencerData) Lines(prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)
	ln.Add("%s(%d/%d)", sd.Name(), sd.ChainHeight(), sd.BranchHeight())
	ln.Add("Minimum fee: %d", sd.MinimumFee())
	ln.Add("Pace: %d", sd.Pace())
	ln.Add("Inflation margin promille: %d", sd.InflationProfitMarginPromille())
	ln.Add("Generous: %v", sd.IsGenerous())
	return ln
}
