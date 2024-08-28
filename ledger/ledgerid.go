package ledger

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"
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
		// genesis time unix seconds
		GenesisTimeUnix uint32
		// initial supply of tokens
		InitialSupply uint64
		// ED25519 public key of the controller
		GenesisControllerPublicKey ed25519.PublicKey
		// time tick duration in nanoseconds
		TickDuration time.Duration
		// ----------- begin inflation-related
		// BranchInflationBonusBase inflation bonus
		BranchInflationBonusBase uint64
		// ChainInflationPerTickBase is maximum total inflation per one tick. It is fixed amount for the ledger
		// It is equal to the inflation which generates the whole supply per one tick
		ChainInflationPerTickBase uint64
		// TicksPerInflationEpoch usually equal to 1 standard year with 365 days
		TicksPerInflationEpoch uint64
		// ChainInflationOpportunitySlots maximum gap between chain outputs for the non-zero inflation
		ChainInflationOpportunitySlots uint64
		// ----------- end inflation-related
		// VBCost
		VBCost uint64
		// number of ticks between non-sequencer transactions
		TransactionPace byte
		// number of ticks between sequencer transactions
		TransactionPaceSequencer byte
		// this limits number of sequencers in the network. Reasonable amount would be few hundreds of sequencers
		MinimumAmountOnSequencer uint64
		// limit maximum number of endorsements. For determinism
		MaxNumberOfEndorsements uint64
		// PreBranchConsolidationTicks enforces endorsement-only constraint for specified amount of ticks
		// before the slot boundary. It means, sequencer transaction can have only one input, its own predecessor
		// for any transaction with timestamp ticks > MaxTickValueInSlot - PreBranchConsolidationTicks
		// value 0 of PreBranchConsolidationTicks effectively means no constraint
		PreBranchConsolidationTicks uint8
	}

	// IdentityDataYAMLAble structure for canonical YAMLAble marshaling
	IdentityDataYAMLAble struct {
		GenesisTimeUnix                uint32 `yaml:"genesis_time_unix"`
		InitialSupply                  uint64 `yaml:"initial_supply"`
		GenesisControllerPublicKey     string `yaml:"genesis_controller_public_key"`
		TimeTickDurationNanosec        int64  `yaml:"time_tick_duration_nanosec"`
		VBCost                         uint64 `yaml:"vb_cost"`
		TransactionPace                byte   `yaml:"transaction_pace"`
		TransactionPaceSequencer       byte   `yaml:"transaction_pace_sequencer"`
		BranchInflationBonusBase       uint64 `yaml:"branch_inflation_bonus_base"`
		ChainInflationPerTickBase      uint64 `yaml:"chain_inflation_per_tick_base"`
		ChainInflationOpportunitySlots uint64 `yaml:"chain_inflation_opportunity_slots"`
		TicksPerInflationEpoch         uint64 `yaml:"ticks_per_inflation_epoch"`
		MinimumAmountOnSequencer       uint64 `yaml:"minimum_amount_on_sequencer"`
		MaxNumberOfEndorsements        uint64 `yaml:"max_number_of_endorsements"`
		PreBranchConsolidationTicks    uint8  `yaml:"pre_branch_consolidation_ticks"`
		Description                    string `yaml:"description"`
		// non-persistent, for control
		GenesisControllerAddress string `yaml:"genesis_controller_address"`
		BootstrapChainID         string `yaml:"bootstrap_chain_id"`
	}
)

const (
	GenesisOutputIndex     = byte(0)
	GenesisStemOutputIndex = byte(1)

	TicksPerSlot = 256
	MaxTickValue = TicksPerSlot - 1
)

func (id *IdentityData) Bytes() []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, id.GenesisTimeUnix)
	util.Assertf(len(id.GenesisControllerPublicKey) == ed25519.PublicKeySize, "id.GenesisControllerPublicKey)==ed25519.PublicKeySize")
	buf.Write(id.GenesisControllerPublicKey)
	_ = binary.Write(&buf, binary.BigEndian, id.InitialSupply)
	_ = binary.Write(&buf, binary.BigEndian, id.TickDuration.Nanoseconds())
	_ = binary.Write(&buf, binary.BigEndian, id.BranchInflationBonusBase)
	_ = binary.Write(&buf, binary.BigEndian, id.ChainInflationPerTickBase)
	_ = binary.Write(&buf, binary.BigEndian, id.ChainInflationOpportunitySlots)
	_ = binary.Write(&buf, binary.BigEndian, id.TicksPerInflationEpoch)
	_ = binary.Write(&buf, binary.BigEndian, id.VBCost)
	_ = binary.Write(&buf, binary.BigEndian, id.TransactionPace)
	_ = binary.Write(&buf, binary.BigEndian, id.TransactionPaceSequencer)
	_ = binary.Write(&buf, binary.BigEndian, id.MinimumAmountOnSequencer)
	_ = binary.Write(&buf, binary.BigEndian, id.MaxNumberOfEndorsements)
	_ = binary.Write(&buf, binary.BigEndian, id.PreBranchConsolidationTicks)
	_ = binary.Write(&buf, binary.BigEndian, uint16(len(id.Description)))
	buf.Write([]byte(id.Description))

	return buf.Bytes()
}

func MustLedgerIdentityDataFromBytes(data []byte) *IdentityData {
	ret, err := IdentityDataFromBytes(data)
	util.AssertNoError(err)
	return ret
}

func IdentityDataFromBytes(data []byte) (*IdentityData, error) {
	ret := &IdentityData{}
	rdr := bytes.NewReader(data)

	var size16 uint16
	var n int

	err := binary.Read(rdr, binary.BigEndian, &ret.GenesisTimeUnix)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}

	buf := make([]byte, ed25519.PublicKeySize)
	n, err = rdr.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}
	if n != ed25519.PublicKeySize {
		return nil, fmt.Errorf("IdentityDataFromBytes: wrong data size")
	}
	ret.GenesisControllerPublicKey = buf

	err = binary.Read(rdr, binary.BigEndian, &ret.InitialSupply)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}

	var bufNano int64
	err = binary.Read(rdr, binary.BigEndian, &bufNano)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}
	ret.TickDuration = time.Duration(bufNano)

	err = binary.Read(rdr, binary.BigEndian, &ret.BranchInflationBonusBase)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}

	err = binary.Read(rdr, binary.BigEndian, &ret.ChainInflationPerTickBase)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}

	err = binary.Read(rdr, binary.BigEndian, &ret.TicksPerInflationEpoch)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}

	err = binary.Read(rdr, binary.BigEndian, &ret.ChainInflationOpportunitySlots)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}

	err = binary.Read(rdr, binary.BigEndian, &ret.VBCost)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}

	err = binary.Read(rdr, binary.BigEndian, &ret.TransactionPace)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}

	err = binary.Read(rdr, binary.BigEndian, &ret.TransactionPaceSequencer)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}

	err = binary.Read(rdr, binary.BigEndian, &ret.MinimumAmountOnSequencer)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}

	err = binary.Read(rdr, binary.BigEndian, &ret.MaxNumberOfEndorsements)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}

	err = binary.Read(rdr, binary.BigEndian, &ret.PreBranchConsolidationTicks)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}

	err = binary.Read(rdr, binary.BigEndian, &size16)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}
	buf = make([]byte, size16)
	n, err = rdr.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("IdentityDataFromBytes: %v", err)
	}
	if n != int(size16) {
		return nil, fmt.Errorf("IdentityDataFromBytes: wrong data size")
	}
	ret.Description = string(buf)

	if rdr.Len() > 0 {
		return nil, fmt.Errorf("IdentityDataFromBytes: not all bytes have been read")
	}
	return ret, nil
}

func (id *IdentityData) GenesisTime() time.Time {
	return time.Unix(int64(id.GenesisTimeUnix), 0)
}

func (id *IdentityData) GenesisTimeUnixNano() int64 {
	return time.Unix(int64(id.GenesisTimeUnix), 0).UnixNano()
}

func (id *IdentityData) Hash() [32]byte {
	return blake2b.Sum256(id.Bytes())
}

func (id *IdentityData) GenesisControlledAddress() AddressED25519 {
	return AddressED25519FromPublicKey(id.GenesisControllerPublicKey)
}

func (id *IdentityData) TimeFromRealTime(nowis time.Time) Time {
	util.Assertf(!nowis.Before(id.GenesisTime()), "!nowis.Before(id.GenesisTimeUnix)")
	sinceGenesisNano := nowis.UnixNano() - id.GenesisTimeUnixNano()

	ret, err := TimeFromTicksSinceGenesis(sinceGenesisNano / int64(id.TickDuration))
	util.AssertNoError(err)
	return ret
}

func (id *IdentityData) SlotDuration() time.Duration {
	return id.TickDuration * time.Duration(TicksPerSlot)
}

func (id *IdentityData) SlotsPerDay() int {
	return int(24 * time.Hour / id.SlotDuration())
}

func (id *IdentityData) SlotsPerYear() int {
	return 365 * id.SlotsPerDay()
}

func (id *IdentityData) TicksPerYear() int {
	return id.SlotsPerYear() * TicksPerSlot
}

func (id *IdentityData) OriginChainID() ChainID {
	oid := GenesisOutputID()
	return MakeOriginChainID(&oid)
}

func (id *IdentityData) IsPreBranchConsolidationTimestamp(ts Time) bool {
	return ts.Tick() > MaxTickValue-id.PreBranchConsolidationTicks
}

func (id *IdentityData) String() string {
	return string(id.YAMLAble().YAML())
}

func (id *IdentityData) Lines(prefix ...string) *lines.Lines {
	originChainID := id.OriginChainID()
	return lines.New(prefix...).
		Add("Description: '%s'", id.Description).
		Add("Initial supply: %s", util.Th(id.InitialSupply)).
		Add("Genesis controller public key: %s", hex.EncodeToString(id.GenesisControllerPublicKey)).
		Add("Genesis controller address: %s", id.GenesisControlledAddress().String()).
		Add("Genesis Unix time: %d (%s)", id.GenesisTimeUnix, id.GenesisTime().Format(time.RFC3339)).
		Add("Time tick duration: %v", id.TickDuration).
		Add("Chain inflation per tick base: %s", util.Th(id.ChainInflationPerTickBase)).
		Add("Branch inflation bonus base: %s", util.Th(id.BranchInflationBonusBase)).
		Add("Chain inflation opportunity slots: %v", id.ChainInflationOpportunitySlots).
		Add("Ticks per inflation epoch: %s", util.Th(id.TicksPerInflationEpoch)).
		Add("Pre-branch consolidation ticks: %v", id.PreBranchConsolidationTicks).
		Add("Minimum amount on sequencer: %s", util.Th(id.MinimumAmountOnSequencer)).
		Add("Transaction pace: %d", id.TransactionPace).
		Add("Sequencer pace: %d", id.TransactionPaceSequencer).
		Add("VB cost: %d", id.VBCost).
		Add("Origin chain ID (calculated): %s", originChainID.String())
}

func (id *IdentityData) YAMLAble() *IdentityDataYAMLAble {
	chainID := id.OriginChainID()
	return &IdentityDataYAMLAble{
		GenesisTimeUnix:                id.GenesisTimeUnix,
		GenesisControllerPublicKey:     hex.EncodeToString(id.GenesisControllerPublicKey),
		InitialSupply:                  id.InitialSupply,
		TimeTickDurationNanosec:        id.TickDuration.Nanoseconds(),
		BranchInflationBonusBase:       id.BranchInflationBonusBase,
		VBCost:                         id.VBCost,
		TransactionPace:                id.TransactionPace,
		TransactionPaceSequencer:       id.TransactionPaceSequencer,
		ChainInflationPerTickBase:      id.ChainInflationPerTickBase,
		ChainInflationOpportunitySlots: id.ChainInflationOpportunitySlots,
		TicksPerInflationEpoch:         id.TicksPerInflationEpoch,
		GenesisControllerAddress:       id.GenesisControlledAddress().String(),
		MinimumAmountOnSequencer:       id.MinimumAmountOnSequencer,
		MaxNumberOfEndorsements:        id.MaxNumberOfEndorsements,
		PreBranchConsolidationTicks:    id.PreBranchConsolidationTicks,
		BootstrapChainID:               chainID.StringHex(),
		Description:                    id.Description,
	}
}

func (id *IdentityData) TimeConstantsToString() string {
	nowis := time.Now()
	timestampNowis := id.TimeFromRealTime(nowis)

	//{
	//	// TODO assertion sometimes fails due to integer arithmetics at nano level
	//	util.Assertf(nowis.UnixNano()-timestampNowis.UnixNano() < int64(TickDuration()),
	//		"assertion sometimes fails: nowis.UnixNano()(%d)-timestampNowis.UnixNano()(%d) = %d < int64(TickDuration())(%d)",
	//		nowis.UnixNano(), timestampNowis.UnixNano(), nowis.UnixNano()-timestampNowis.UnixNano(), int64(TickDuration()))
	//}

	maxYears := MaxSlot / (id.SlotsPerDay() * 365)
	return lines.New().
		Add("TickDuration = %v", id.TickDuration).
		Add("SlotDuration = %v", id.SlotDuration()).
		Add("SlotsPerDay = %d", id.SlotsPerDay()).
		Add("MaxYears = %d", maxYears).
		Add("seconds per year = %d", 60*60*24*365).
		Add("timestamp GenesisTime = %v", id.GenesisTime()).
		Add("nowis %v", nowis).
		Add("nowis nano %d", nowis.UnixNano()).
		Add("GenesisTimeUnix = %d", id.GenesisTimeUnix).
		Add("GenesisTimeUnixNano = %d", id.GenesisTimeUnixNano()).
		Add("timestampNowis = %s ", timestampNowis.String()).
		Add("timestampNowis.Time() = %v ", timestampNowis.Time()).
		Add("timestampNowis.Time().UnixNano() = %v ", timestampNowis.Time().UnixNano()).
		Add("timestampNowis.UnixNano() = %v ", timestampNowis.UnixNano()).
		Add("rounding: nowis.UnixNano() - timestampNowis.UnixNano() = %d", nowis.UnixNano()-timestampNowis.UnixNano()).
		Add("tick duration nano = %d", int64(TickDuration())).
		String()
}

func (id *IdentityData) YAML() []byte {
	return id.YAMLAble().YAML()
}

const stateIDFileComment = `# This is Proxima ledger identity file.
# It contains public Proxima ledger constants set at genesis. 
# The ledger identity file is used to create genesis ledger state for Proxima nodes.
# Private key of the controller should be known only to the creator of the genesis ledger state.
# Public key of the controller identifies originator of the ledger for its lifetime.
# 'genesis_controller_address' is computed from the public key of the controller
# 'bootstrap_chain_id' is a constant, i.e. same for all ledgers
`

func (id *IdentityDataYAMLAble) YAML() []byte {
	var buf bytes.Buffer
	data, err := yaml.Marshal(id)
	util.AssertNoError(err)
	buf.WriteString(stateIDFileComment)
	buf.Write(data)
	return buf.Bytes()
}

func (id *IdentityDataYAMLAble) stateIdentityData() (*IdentityData, error) {
	var err error
	ret := &IdentityData{}
	ret.GenesisTimeUnix = id.GenesisTimeUnix
	ret.GenesisControllerPublicKey, err = hex.DecodeString(id.GenesisControllerPublicKey)
	ret.InitialSupply = id.InitialSupply
	if err != nil {
		return nil, err
	}
	if len(ret.GenesisControllerPublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("wrong public key")
	}
	ret.TickDuration = time.Duration(id.TimeTickDurationNanosec)
	ret.BranchInflationBonusBase = id.BranchInflationBonusBase
	ret.VBCost = id.VBCost
	ret.TransactionPace = id.TransactionPace
	ret.TransactionPaceSequencer = id.TransactionPaceSequencer
	ret.ChainInflationPerTickBase = id.ChainInflationPerTickBase
	ret.ChainInflationOpportunitySlots = id.ChainInflationOpportunitySlots
	ret.TicksPerInflationEpoch = id.TicksPerInflationEpoch
	ret.MinimumAmountOnSequencer = id.MinimumAmountOnSequencer
	ret.MaxNumberOfEndorsements = id.MaxNumberOfEndorsements
	ret.PreBranchConsolidationTicks = id.PreBranchConsolidationTicks
	ret.Description = id.Description

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

// GenesisTransactionID independent on any ledger constants
func GenesisTransactionID() *TransactionID {
	ret := NewTransactionID(Time{}, TransactionIDShort{}, true)
	return &ret
}

// GenesisOutputID independent on ledger constants, except GenesisOutputIndex which is byte(0)
func GenesisOutputID() (ret OutputID) {
	// we are placing sequencer flag = true into the genesis tx ID to please sequencer constraint
	// of the origin branch transaction. It is the only exception
	ret = NewOutputID(GenesisTransactionID(), GenesisOutputIndex)
	return
}

// GenesisStemOutputID independent on ledger constants, except GenesisStemOutputIndex which is byte(1)
func GenesisStemOutputID() (ret OutputID) {
	ret = NewOutputID(GenesisTransactionID(), GenesisStemOutputIndex)
	return
}
