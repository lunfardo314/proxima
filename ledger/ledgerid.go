package ledger

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"golang.org/x/crypto/blake2b"
	"gopkg.in/yaml.v2"
)

// IdentityData is provided at genesis and will remain immutable during lifetime
// All integers are serialized as big-endian
type (
	IdentityData struct {
		// arbitrary string up 255 bytes
		Description string
		// initial supply of tokens
		InitialSupply uint64
		// ED25519 public key of the controller
		GenesisControllerPublicKey ed25519.PublicKey
		// baseline time unix nanoseconds
		BaselineTime time.Time
		// time tick duration in nanoseconds
		TickDuration time.Duration
		// max time tick value in the slot. Up to 256 time ticks per time slot, default 100
		MaxTickValueInSlot uint8
		// time slot of the genesis
		GenesisSlot Slot
		// ----------- inflation-related
		// InitialBranchInflation inflation bonus. Inflated every year by BranchBonusYearlyGrowthPromille
		InitialBranchBonus uint64
		// BranchBonusYearlyGrowthPromille branch bonus is inflated y/y
		BranchBonusYearlyGrowthPromille uint16
		// approx one year in slots. Default 2_289_600
		SlotsPerLedgerYear uint32
		// ChainInflationPerTickFractionBase and HalvingYears
		ChainInflationPerTickFractionBase uint64
		ChainInflationHalvingYears        byte
		// VBCost
		VBCost uint64
		// number of ticks between non-sequencer transactions
		TransactionPace byte
		// number of ticks between sequencer transactions
		TransactionPaceSequencer byte
	}

	// IdentityDataYAMLAble structure for canonical YAMLAble marshaling
	IdentityDataYAMLAble struct {
		Description                       string `yaml:"description"`
		InitialSupply                     uint64 `yaml:"initial_supply"`
		GenesisControllerPublicKey        string `yaml:"genesis_controller_public_key"`
		BaselineTime                      int64  `yaml:"baseline_time"`
		TimeTickDuration                  int64  `yaml:"time_tick_duration"`
		MaxTimeTickValueInTimeSlot        uint8  `yaml:"max_time_tick_value_in_time_slot"`
		GenesisTimeSlot                   uint32 `yaml:"genesis_time_slot"`
		InitialBranchBonus                uint64 `yaml:"initial_branch_bonus"`
		BranchBonusYearlyGrowthPromille   uint16 `yaml:"branch_bonus_yearly_growth_promille"`
		SlotsPerLedgerYear                uint32 `yaml:"slots_per_ledger_year"`
		VBCost                            uint64 `yaml:"vb_cost"`
		TransactionPace                   byte   `yaml:"transaction_pace"`
		TransactionPaceSequencer          byte   `yaml:"transaction_pace_sequencer"`
		ChainInflationHalvingYears        byte   `yaml:"chain_inflation_halving_years"`
		ChainInflationPerTickFractionBase uint64 `yaml:"chain_inflation_per_tick_base"`
		// non-persistent, for control
		GenesisControllerAddress string `yaml:"genesis_controller_address"`
		BootstrapChainID         string `yaml:"bootstrap_chain_id"`
	}
)

const (
	InitialSupplyOutputIndex = byte(0)
	StemOutputIndex          = byte(1)
)

func (id *IdentityData) Bytes() []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, uint16(len(id.Description)))
	buf.Write([]byte(id.Description))
	util.Assertf(len(id.GenesisControllerPublicKey) == ed25519.PublicKeySize, "id.GenesisControllerPublicKey)==ed25519.PublicKeySize")
	buf.Write(id.GenesisControllerPublicKey)
	_ = binary.Write(&buf, binary.BigEndian, id.InitialSupply)
	_ = binary.Write(&buf, binary.BigEndian, id.BaselineTime.UnixNano())
	_ = binary.Write(&buf, binary.BigEndian, id.TickDuration.Nanoseconds())
	_ = binary.Write(&buf, binary.BigEndian, id.MaxTickValueInSlot)
	_ = binary.Write(&buf, binary.BigEndian, id.GenesisSlot)
	_ = binary.Write(&buf, binary.BigEndian, id.SlotsPerLedgerYear)
	_ = binary.Write(&buf, binary.BigEndian, id.InitialBranchBonus)
	_ = binary.Write(&buf, binary.BigEndian, id.BranchBonusYearlyGrowthPromille)
	_ = binary.Write(&buf, binary.BigEndian, id.ChainInflationHalvingYears)
	_ = binary.Write(&buf, binary.BigEndian, id.ChainInflationPerTickFractionBase)
	_ = binary.Write(&buf, binary.BigEndian, id.VBCost)
	_ = binary.Write(&buf, binary.BigEndian, id.TransactionPace)
	_ = binary.Write(&buf, binary.BigEndian, id.TransactionPaceSequencer)

	return buf.Bytes()
}

func MustLedgerIdentityDataFromBytes(data []byte) *IdentityData {
	ret := &IdentityData{}
	rdr := bytes.NewReader(data)
	var size16 uint16

	err := binary.Read(rdr, binary.BigEndian, &size16)
	util.AssertNoError(err)
	buf := make([]byte, size16)
	n, err := rdr.Read(buf)
	util.AssertNoError(err)
	util.Assertf(n == int(size16), "wrong data size")
	ret.Description = string(buf)

	buf = make([]byte, ed25519.PublicKeySize)
	n, err = rdr.Read(buf)
	util.AssertNoError(err)
	util.Assertf(n == ed25519.PublicKeySize, "wrong data size")
	ret.GenesisControllerPublicKey = buf

	err = binary.Read(rdr, binary.BigEndian, &ret.InitialSupply)
	util.AssertNoError(err)

	var bufNano int64
	err = binary.Read(rdr, binary.BigEndian, &bufNano)
	util.AssertNoError(err)
	ret.BaselineTime = time.Unix(0, bufNano)

	err = binary.Read(rdr, binary.BigEndian, &bufNano)
	util.AssertNoError(err)
	ret.TickDuration = time.Duration(bufNano)

	err = binary.Read(rdr, binary.BigEndian, &ret.MaxTickValueInSlot)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.GenesisSlot)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.SlotsPerLedgerYear)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.InitialBranchBonus)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.BranchBonusYearlyGrowthPromille)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.ChainInflationHalvingYears)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.ChainInflationPerTickFractionBase)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.VBCost)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.TransactionPace)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.TransactionPaceSequencer)
	util.AssertNoError(err)

	util.Assertf(rdr.Len() == 0, "not all bytes has been read")
	return ret
}

func (id *IdentityData) Hash() [32]byte {
	return blake2b.Sum256(id.Bytes())
}

func (id *IdentityData) GenesisControlledAddress() AddressED25519 {
	return AddressED25519FromPublicKey(id.GenesisControllerPublicKey)
}

func (id *IdentityData) TimeFromRealTime(nowis time.Time) Time {
	util.Assertf(!nowis.Before(id.BaselineTime), "!nowis.Before(id.BaselineTime)")
	i := nowis.UnixNano() - id.BaselineTime.UnixNano()
	e := i / int64(id.SlotDuration())
	util.Assertf(e <= math.MaxUint32, "TimeFromRealTime: e <= math.MaxUint32")
	util.Assertf(uint32(e)&0xc0000000 == 0, "TimeFromRealTime: two highest bits must be 0. Wrong constants")
	s := i % int64(id.SlotDuration())
	s = s / int64(id.TickDuration)
	util.Assertf(s < DefaultTicksPerSlot, "TimeFromRealTime: s < DefaultTicksPerSlot")
	return MustNewLedgerTime(Slot(uint32(e)), Tick(byte(s)))
}

func (id *IdentityData) SlotDuration() time.Duration {
	return id.TickDuration * time.Duration(id.TicksPerSlot())
}

func (id *IdentityData) TicksPerSlot() int {
	return int(id.MaxTickValueInSlot) + 1
}

func (id *IdentityData) OriginChainID() ChainID {
	oid := GenesisOutputID(id.GenesisSlot)
	return OriginChainID(&oid)
}

func (id *IdentityData) String() string {
	return id.Lines().String()
}

func (id *IdentityData) Lines(prefix ...string) *lines.Lines {
	originChainID := id.OriginChainID()
	return lines.New(prefix...).
		Add("Description: '%s'", id.Description).
		Add("Initial supply: %s", util.GoTh(id.InitialSupply)).
		Add("Genesis controller address: %s", id.GenesisControlledAddress().String()).
		Add("Baseline time: %s", id.BaselineTime.Format(time.RFC3339)).
		Add("Time tick duration: %v", id.TickDuration).
		Add("Time ticks per time slot: %d", id.TicksPerSlot()).
		Add("Genesis time slot: %d", id.GenesisSlot).
		Add("Origin chain ID: %s", originChainID.String())
}

func (id *IdentityData) YAMLAble() *IdentityDataYAMLAble {
	chainID := id.OriginChainID()
	return &IdentityDataYAMLAble{
		Description:                       id.Description,
		InitialSupply:                     id.InitialSupply,
		GenesisControllerPublicKey:        hex.EncodeToString(id.GenesisControllerPublicKey),
		BaselineTime:                      id.BaselineTime.UnixNano(),
		TimeTickDuration:                  id.TickDuration.Nanoseconds(),
		MaxTimeTickValueInTimeSlot:        id.MaxTickValueInSlot,
		GenesisTimeSlot:                   uint32(id.GenesisSlot),
		InitialBranchBonus:                id.InitialBranchBonus,
		BranchBonusYearlyGrowthPromille:   id.BranchBonusYearlyGrowthPromille,
		VBCost:                            id.VBCost,
		TransactionPace:                   id.TransactionPace,
		TransactionPaceSequencer:          id.TransactionPaceSequencer,
		SlotsPerLedgerYear:                id.SlotsPerLedgerYear,
		ChainInflationHalvingYears:        id.ChainInflationHalvingYears,
		ChainInflationPerTickFractionBase: id.ChainInflationPerTickFractionBase,
		GenesisControllerAddress:          id.GenesisControlledAddress().String(),
		BootstrapChainID:                  chainID.StringHex(),
	}
}

func (id *IdentityData) TimeHorizonYears() int {
	return (math.MaxUint32 - int(id.GenesisSlot)) / int(id.SlotsPerLedgerYear)
}

func (id *IdentityData) SlotsPerDay() int {
	return int(24 * time.Hour / id.SlotDuration())
}

func (id *IdentityData) TimeConstantsToString() string {
	return lines.New().
		Add("TickDuration = %v", id.TickDuration).
		Add("TicksPerSlot = %d", id.TicksPerSlot()).
		Add("SlotDuration = %v", id.SlotDuration()).
		Add("SlotsPerDay = %v", id.SlotsPerDay()).
		Add("GenesisSlot = %d (%.2f %% of max)", id.GenesisSlot, float32(id.GenesisSlot)*100/float32(math.MaxUint32)).
		Add("TimeHorizonYears = %d", id.TimeHorizonYears()).
		Add("SlotsPerLedgerYear = %d", id.SlotsPerLedgerYear).
		Add("seconds per year = %d", 60*60*24*365).
		Add("BaselineTime = %v", id.BaselineTime).
		Add("timestamp BaselineTime = %s", id.TimeFromRealTime(id.BaselineTime)).
		Add("timestamp now = %s, now is %v", id.TimeFromRealTime(time.Now()).String(), time.Now()).
		String()
}

func (id *IdentityData) YAML() []byte {
	return id.YAMLAble().YAML()
}

const stateIDComment = `# This file contains Proxima ledger identity data.
# It will be used to create genesis ledger state for the Proxima network.
# The ledger identity file does not contain secrets, it is public.
# The data in the file must match genesis controller private key and hardcoded protocol constants.
# Once used to create genesis, identity data should never be modified.
# Values 'genesis_controller_address' and 'bootstrap_chain_id' are computed values used for control
`

func (id *IdentityDataYAMLAble) YAML() []byte {
	var buf bytes.Buffer
	data, err := yaml.Marshal(id)
	util.AssertNoError(err)
	buf.WriteString(stateIDComment)
	buf.Write(data)
	return buf.Bytes()
}

func (id *IdentityDataYAMLAble) stateIdentityData() (*IdentityData, error) {
	var err error
	ret := &IdentityData{}
	ret.Description = id.Description
	ret.InitialSupply = id.InitialSupply
	ret.GenesisControllerPublicKey, err = hex.DecodeString(id.GenesisControllerPublicKey)
	if err != nil {
		return nil, err
	}
	if len(ret.GenesisControllerPublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("wrong public key")
	}
	ret.BaselineTime = time.Unix(0, id.BaselineTime)
	ret.TickDuration = time.Duration(id.TimeTickDuration)
	ret.MaxTickValueInSlot = id.MaxTimeTickValueInTimeSlot
	ret.GenesisSlot = Slot(id.GenesisTimeSlot)
	ret.InitialBranchBonus = id.InitialBranchBonus
	ret.BranchBonusYearlyGrowthPromille = id.BranchBonusYearlyGrowthPromille
	ret.VBCost = id.VBCost
	ret.TransactionPace = id.TransactionPace
	ret.TransactionPaceSequencer = id.TransactionPaceSequencer
	ret.SlotsPerLedgerYear = id.SlotsPerLedgerYear
	ret.ChainInflationPerTickFractionBase = id.ChainInflationPerTickFractionBase
	ret.ChainInflationHalvingYears = id.ChainInflationHalvingYears

	// control
	if AddressED25519FromPublicKey(ret.GenesisControllerPublicKey).String() != id.GenesisControllerAddress {
		return nil, fmt.Errorf("YAML data inconsistency: address and public key does not match")
	}
	chainID := ret.OriginChainID()
	if id.BootstrapChainID != chainID.StringHex() {
		return nil, fmt.Errorf("YAML data inconsistency: bootstrap chain ID does not match")
	}
	return ret, nil
}

func StateIdentityDataFromYAML(yamlData []byte) (*IdentityData, error) {
	yamlAble := &IdentityDataYAMLAble{}
	if err := yaml.Unmarshal(yamlData, &yamlAble); err != nil {
		return nil, err
	}
	return yamlAble.stateIdentityData()
}

func GenesisTransactionID(genesisTimeSlot Slot) *TransactionID {
	ret := NewTransactionID(MustNewLedgerTime(genesisTimeSlot, 0), All0TransactionHash, true, true)
	return &ret
}

func GenesisOutputID(e Slot) (ret OutputID) {
	// we are placing sequencer flag = true into the genesis tx ID to please sequencer constraint
	// of the origin branch transaction. It is the only exception
	ret = NewOutputID(GenesisTransactionID(e), InitialSupplyOutputIndex)
	return
}

func StemOutputID(e Slot) (ret OutputID) {
	ret = NewOutputID(GenesisTransactionID(e), StemOutputIndex)
	return
}
