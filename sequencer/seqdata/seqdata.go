package seqdata

import (
	"encoding/json"
	"math"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

// SequencerData holds sequencer configuration metadata stored in sequencer outputs.
// Serialized as compact JSON (short tags, omitempty).
type SequencerData struct {
	SeqName          string `json:"n,omitempty"`
	MinFee           uint64 `json:"f,omitempty"`
	ProfitPromille   uint16 `json:"m,omitempty"`
	Greedy           bool   `json:"g,omitempty"`
	PaceValue        byte   `json:"p,omitempty"`
	IgnoreFreezeBound bool  `json:"u,omitempty"`
}

func New() *SequencerData {
	return &SequencerData{}
}

func (sd *SequencerData) Clone(modify ...func(sdUpdated *SequencerData)) *SequencerData {
	cp := *sd
	ret := &cp
	if len(modify) > 0 {
		modify[0](ret)
	}
	return ret
}

func (sd *SequencerData) Name() string {
	return sd.SeqName
}

func (sd *SequencerData) SetName(name string) *SequencerData {
	sd.SeqName = name
	return sd
}

func (sd *SequencerData) MinimumFee() uint64 {
	return sd.MinFee
}

func (sd *SequencerData) SetMinimumFee(fee uint64) *SequencerData {
	sd.MinFee = fee
	return sd
}

func (sd *SequencerData) InflationProfitMarginPromille() uint16 {
	return sd.ProfitPromille
}

func (sd *SequencerData) SetSeqProfitMarginPromille(margin uint16) *SequencerData {
	sd.ProfitPromille = margin
	return sd
}

func (sd *SequencerData) InflationProfitMargin(amount uint64) uint64 {
	p := sd.InflationProfitMarginPromille()
	if p == 0 {
		return 0
	}
	if p > 1000 {
		return amount
	}
	if amount > math.MaxUint64/uint64(p) {
		return 0
	}
	return (amount * uint64(p)) / 1000
}

func (sd *SequencerData) Pace() byte {
	return sd.PaceValue
}

func (sd *SequencerData) SetPace(pace byte) *SequencerData {
	sd.PaceValue = pace
	return sd
}

func (sd *SequencerData) SetGreedy(greedy bool) *SequencerData {
	sd.Greedy = greedy
	return sd
}

func (sd *SequencerData) IsGreedy() bool {
	return sd.Greedy
}

func (sd *SequencerData) SetIgnoreFreezeBound(ignore bool) *SequencerData {
	sd.IgnoreFreezeBound = ignore
	return sd
}

func (sd *SequencerData) IsIgnoreFreezeBound() bool {
	return sd.IgnoreFreezeBound
}

// Bytes returns compact JSON serialization (no extra whitespace).
func (sd *SequencerData) Bytes() []byte {
	data, err := json.Marshal(sd)
	util.AssertNoError(err, "SequencerData.Bytes:")
	return data
}

// FromBytes deserializes SequencerData from JSON bytes.
func FromBytes(data []byte) (ret SequencerData, err error) {
	if len(data) == 0 {
		return SequencerData{}, nil
	}
	err = json.Unmarshal(data, &ret)
	return
}

// Lines returns pretty-formatted JSON representation.
func (sd *SequencerData) Lines(prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)
	data, err := json.MarshalIndent(sd, "", "  ")
	if err != nil {
		ln.Add("(json marshal error: %v)", err)
		return ln
	}
	ln.Add("%s", string(data))
	return ln
}
