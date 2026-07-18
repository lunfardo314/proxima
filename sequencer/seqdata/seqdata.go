package seqdata

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

// SequencerData holds sequencer configuration metadata stored in sequencer outputs.
// Serialized as compact JSON (omitempty).
//
// Freeze bounds are opt-in: absent freeze_bounds means the coverage contribution
// upper bound is not enforced when freezing delegations.
type SequencerData struct {
	SeqName             string `json:"name,omitempty"`
	MinFee              uint64 `json:"fee,omitempty"`
	ProfitPromille      uint16 `json:"profit_cut,omitempty"`
	Greedy              bool   `json:"greedy,omitempty"`
	PaceValue           byte   `json:"pace,omitempty"`
	EnforceFreezeBounds bool   `json:"freeze_bounds,omitempty"`

	// extra carries keys this build does not recognise. They are parsed
	// verbatim and re-emitted on serialization, so updating a sequencer from a
	// build that predates a key does not silently erase it.
	extra map[string]json.RawMessage
}

func New() *SequencerData {
	return &SequencerData{}
}

func (sd *SequencerData) Clone(modify ...func(sdUpdated *SequencerData)) *SequencerData {
	cp := *sd
	ret := &cp
	if len(sd.extra) > 0 {
		ret.extra = make(map[string]json.RawMessage, len(sd.extra))
		for k, v := range sd.extra {
			ret.extra[k] = v
		}
	}
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

func (sd *SequencerData) SetEnforceFreezeBounds(enforce bool) *SequencerData {
	sd.EnforceFreezeBounds = enforce
	return sd
}

func (sd *SequencerData) IsFreezeBoundsEnforced() bool {
	return sd.EnforceFreezeBounds
}

// alias sheds the custom JSON methods so the struct fields can be
// (un)marshalled by the reflect-based encoder without recursing.
type alias SequencerData

// MarshalJSON emits the known fields (omitempty) merged with any unrecognised
// keys. Always goes through a map so the key order is the map encoder's sorted
// order in every case — these bytes live in a UTXO, so the same logical value
// must always produce the same bytes.
func (sd SequencerData) MarshalJSON() ([]byte, error) {
	known, err := json.Marshal(alias(sd))
	if err != nil {
		return nil, err
	}
	m := make(map[string]json.RawMessage)
	if err = json.Unmarshal(known, &m); err != nil {
		return nil, err
	}
	for k, v := range sd.extra {
		// a known key always wins; extra only fills what this build lacks
		if _, taken := m[k]; !taken {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// UnmarshalJSON fills the known fields and retains everything else in extra.
func (sd *SequencerData) UnmarshalJSON(data []byte) error {
	var known alias
	if err := json.Unmarshal(data, &known); err != nil {
		return err
	}
	*sd = SequencerData(known)

	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	for _, k := range knownKeys {
		delete(m, k)
	}
	if len(m) > 0 {
		sd.extra = m
	}
	return nil
}

// knownKeys lists every tag this build owns. Listed explicitly rather than
// derived by re-marshalling, because omitempty hides zero-valued fields and an
// explicitly zero known key would then be misfiled as unrecognised.
var knownKeys = []string{"name", "fee", "profit_cut", "greedy", "pace", "freeze_bounds"}

// Bytes returns compact JSON serialization (no extra whitespace).
func (sd *SequencerData) Bytes() []byte {
	data, err := json.Marshal(sd)
	util.AssertNoError(err, "SequencerData.Bytes:")
	return data
}

// FromBytes deserializes SequencerData from JSON bytes. Unrecognised keys are
// accepted and retained.
func FromBytes(data []byte) (ret SequencerData, err error) {
	if len(data) == 0 {
		return SequencerData{}, nil
	}
	err = json.Unmarshal(data, &ret)
	return
}

// Lines returns pretty-formatted JSON representation. Each JSON line is added
// separately so the caller's prefix indents the whole block, not just its
// first line.
func (sd *SequencerData) Lines(prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)
	data, err := json.MarshalIndent(sd, "", "  ")
	if err != nil {
		ln.Add("(json marshal error: %v)", err)
		return ln
	}
	for _, l := range strings.Split(string(data), "\n") {
		ln.Add("%s", l)
	}
	return ln
}
