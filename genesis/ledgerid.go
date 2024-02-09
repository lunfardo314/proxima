package genesis

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"golang.org/x/crypto/blake2b"
	"gopkg.in/yaml.v2"
)

// LedgerIdentityData is provided at genesis and will remain immutable during lifetime
// All integers are serialized as big-endian
type (
	LedgerIdentityData struct {
		// core constraint library hash. For checking of ledger version compatibility with the node
		CoreLedgerConstraintsHash [32]byte
		// arbitrary string up 255 bytes
		Description string
		// initial supply of tokens
		InitialSupply uint64
		// ED25519 public key of the controller
		GenesisControllerPublicKey ed25519.PublicKey
		// baseline time unix nanoseconds
		BaselineTime time.Time
		// time tick duration in nanoseconds
		TimeTickDuration time.Duration
		// max time tick value in the slot. Up to 256 time ticks per time slot, default 100
		MaxTickValueInSlot uint8
		// time slot of the genesis
		GenesisSlot ledger.Slot
		// ----------- inflation-related
		// InitialBranchInflation inflation bonus. Inflated every year by AnnualBranchBonusInflationPromille
		InitialBranchBonus uint64
		// AnnualBranchBonusInflationPromille branch bonus is inflated y/y
		AnnualBranchBonusInflationPromille uint16
		// approx one year in slots. Default 2_289_600
		SlotsPerLedgerYear uint32
		// ChainInflationPerTickFraction fractions year-over-year. SlotsPerLedgerYear is used as year duration
		// The last value in the list is repeated infinitely
		// Is used as inflation for one tick
		ChainInflationPerTickFractionYoY []uint64
	}

	// ledgerIdentityDataYAMLable structure for canonical yamlAble marshaling
	ledgerIdentityDataYAMLable struct {
		Description                string `yaml:"description"`
		InitialSupply              uint64 `yaml:"initial_supply"`
		GenesisControllerPublicKey string `yaml:"genesis_controller_public_key"`
		BaselineTime               int64  `yaml:"baseline_time"`
		TimeTickDuration           int64  `yaml:"time_tick_duration"`
		MaxTimeTickValueInTimeSlot uint8  `yaml:"max_time_tick_value_in_time_slot"`
		GenesisTimeSlot            uint32 `yaml:"genesis_time_slot"`
		CoreLedgerConstraintsHash  string `yaml:"core_ledger_constraints_hash"`
		// for control
		GenesisControllerAddress string `yaml:"genesis_controller_address"`
		BootstrapChainID         string `yaml:"bootstrap_chain_id"`
	}
)

const (
	InitialSupplyOutputIndex = byte(0)
	StemOutputIndex          = byte(1)
)

func (id *LedgerIdentityData) Bytes() []byte {
	var buf bytes.Buffer
	buf.Write(id.CoreLedgerConstraintsHash[:])
	_ = binary.Write(&buf, binary.BigEndian, uint16(len([]byte(id.Description))))
	buf.Write([]byte(id.Description))
	util.Assertf(len(id.GenesisControllerPublicKey) == ed25519.PublicKeySize, "id.GenesisControllerPublicKey)==ed25519.PublicKeySize")
	buf.Write(id.GenesisControllerPublicKey)
	_ = binary.Write(&buf, binary.BigEndian, id.InitialSupply)
	_ = binary.Write(&buf, binary.BigEndian, id.BaselineTime.UnixNano())
	_ = binary.Write(&buf, binary.BigEndian, id.TimeTickDuration.Nanoseconds())
	_ = binary.Write(&buf, binary.BigEndian, id.MaxTickValueInSlot)
	_ = binary.Write(&buf, binary.BigEndian, id.GenesisSlot)
	_ = binary.Write(&buf, binary.BigEndian, id.SlotsPerLedgerYear)
	_ = binary.Write(&buf, binary.BigEndian, id.InitialBranchBonus)
	_ = binary.Write(&buf, binary.BigEndian, id.AnnualBranchBonusInflationPromille)
	util.Assertf(0 < len(id.ChainInflationPerTickFractionYoY) && len(id.ChainInflationPerTickFractionYoY) <= 255, "too long array")
	_ = binary.Write(&buf, binary.BigEndian, byte(len(id.ChainInflationPerTickFractionYoY))) // array length
	for _, v := range id.ChainInflationPerTickFractionYoY {
		_ = binary.Write(&buf, binary.BigEndian, v) // array elements
	}
	return buf.Bytes()
}

func MustLedgerIdentityDataFromBytes(data []byte) *LedgerIdentityData {
	ret := &LedgerIdentityData{}
	rdr := bytes.NewReader(data)
	var ledgerConstraintHash [32]byte
	n, err := rdr.Read(ledgerConstraintHash[:])
	util.AssertNoError(err)
	util.Assertf(n == 32, "wrong data size")
	libraryHash := easyfl.LibraryHash()
	msg := "node's constraint library is incompatible with the multi-state identity\nExpected library hash %s, got %s"
	util.Assertf(libraryHash == ledgerConstraintHash, msg, hex.EncodeToString(libraryHash[:]), hex.EncodeToString(ledgerConstraintHash[:]))

	var size16 uint16

	err = binary.Read(rdr, binary.BigEndian, &size16)
	util.AssertNoError(err)
	buf := make([]byte, size16)
	n, err = rdr.Read(buf)
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
	ret.TimeTickDuration = time.Duration(bufNano)

	err = binary.Read(rdr, binary.BigEndian, &ret.MaxTickValueInSlot)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.GenesisSlot)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.SlotsPerLedgerYear)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.InitialBranchBonus)
	util.AssertNoError(err)

	err = binary.Read(rdr, binary.BigEndian, &ret.AnnualBranchBonusInflationPromille)
	util.AssertNoError(err)

	var size8 byte
	err = binary.Read(rdr, binary.BigEndian, &size8)
	util.AssertNoError(err)

	ret.ChainInflationPerTickFractionYoY = make([]uint64, size8)
	for i := range ret.ChainInflationPerTickFractionYoY {
		err = binary.Read(rdr, binary.BigEndian, &ret.ChainInflationPerTickFractionYoY[i])
		util.AssertNoError(err)
	}

	util.Assertf(rdr.Len() == 0, "not all bytes has been read")
	return ret
}

func (id *LedgerIdentityData) Hash() [32]byte {
	return blake2b.Sum256(id.Bytes())
}

func (id *LedgerIdentityData) GenesisControlledAddress() ledger.AddressED25519 {
	return ledger.AddressED25519FromPublicKey(id.GenesisControllerPublicKey)
}

func (id *LedgerIdentityData) TimeTicksPerTimeSlot() int {
	return int(id.MaxTickValueInSlot) + 1
}

func (id *LedgerIdentityData) OriginChainID() ledger.ChainID {
	oid := InitialSupplyOutputID(id.GenesisSlot)
	return ledger.OriginChainID(&oid)
}

func (id *LedgerIdentityData) String() string {
	return id.Lines().String()
}

func (id *LedgerIdentityData) Lines(prefix ...string) *lines.Lines {
	originChainID := id.OriginChainID()
	return lines.New(prefix...).
		Add("Description: '%s'", id.Description).
		Add("Core ledger constraints hash: %s", hex.EncodeToString(id.CoreLedgerConstraintsHash[:])).
		Add("Initial supply: %s", util.GoTh(id.InitialSupply)).
		Add("Genesis controller address: %s", id.GenesisControlledAddress().String()).
		Add("Baseline time: %s", id.BaselineTime.Format(time.RFC3339)).
		Add("Time tick duration: %v", id.TimeTickDuration).
		Add("Time ticks per time slot: %d", id.TimeTicksPerTimeSlot()).
		Add("Genesis time slot: %d", id.GenesisSlot).
		Add("Origin chain ID: %s", originChainID.String())
}

func (id *LedgerIdentityData) yamlAble() *ledgerIdentityDataYAMLable {
	chainID := id.OriginChainID()
	return &ledgerIdentityDataYAMLable{
		Description:                id.Description,
		InitialSupply:              id.InitialSupply,
		GenesisControllerPublicKey: hex.EncodeToString(id.GenesisControllerPublicKey),
		BaselineTime:               id.BaselineTime.UnixNano(),
		TimeTickDuration:           id.TimeTickDuration.Nanoseconds(),
		MaxTimeTickValueInTimeSlot: id.MaxTickValueInSlot,
		GenesisTimeSlot:            uint32(id.GenesisSlot),
		CoreLedgerConstraintsHash:  hex.EncodeToString(id.CoreLedgerConstraintsHash[:]),
		GenesisControllerAddress:   id.GenesisControlledAddress().String(),
		BootstrapChainID:           chainID.StringHex(),
	}
}

func (id *LedgerIdentityData) YAML() []byte {
	return id.yamlAble().YAML()
}

const stateIDComment = `# This file contains Proxima ledger identity data.
# It will be used to create genesis ledger state for the Proxima network.
# The ledger identity file does not contain secrets, it is public.
# The data in the file must match genesis controller private key and hardcoded protocol constants.
# Except 'description' field, file should not be modified.
# Once used to create genesis, identity data should never be modified.
# Values 'genesis_controller_address' and 'bootstrap_chain_id' are computed values used for control
`

func (id *ledgerIdentityDataYAMLable) YAML() []byte {
	var buf bytes.Buffer
	data, err := yaml.Marshal(id)
	util.AssertNoError(err)
	buf.WriteString(stateIDComment)
	buf.Write(data)
	return buf.Bytes()
}

func (id *ledgerIdentityDataYAMLable) stateIdentityData() (*LedgerIdentityData, error) {
	var err error
	ret := &LedgerIdentityData{}
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
	ret.TimeTickDuration = time.Duration(id.TimeTickDuration)
	ret.MaxTickValueInSlot = id.MaxTimeTickValueInTimeSlot
	ret.GenesisSlot = ledger.Slot(id.GenesisTimeSlot)
	hBin, err := hex.DecodeString(id.CoreLedgerConstraintsHash)
	if err != nil {
		return nil, err
	}
	if len(hBin) != 32 {
		return nil, fmt.Errorf("wrong core library hash")
	}
	copy(ret.CoreLedgerConstraintsHash[:], hBin)

	// control
	if ledger.AddressED25519FromPublicKey(ret.GenesisControllerPublicKey).String() != id.GenesisControllerAddress {
		return nil, fmt.Errorf("YAML data inconsistency: address and public key does not match")
	}
	chainID := ret.OriginChainID()
	if id.BootstrapChainID != chainID.StringHex() {
		return nil, fmt.Errorf("YAML data inconsistency: bootstrap chain ID does not match")
	}
	return ret, nil
}

func StateIdentityDataFromYAML(yamlData []byte) (*LedgerIdentityData, error) {
	yamlAble := &ledgerIdentityDataYAMLable{}
	if err := yaml.Unmarshal(yamlData, &yamlAble); err != nil {
		return nil, err
	}
	return yamlAble.stateIdentityData()
}

const (
	DustPerProxi         = 1_000_000
	InitialSupplyProxi   = 1_000_000_000
	DefaultInitialSupply = InitialSupplyProxi * DustPerProxi
	SlotDuration         = 10 * time.Second
	YearDuration         = 24 * 265 * time.Hour
	SlotsPerYear         = uint32(YearDuration / SlotDuration)

	InitialBranchInflationBonus   = 20_000_000
	AnnualBranchInflationPromille = 40
	ChainInflationFractionPerTick = 400_000_000
)

func DefaultIdentityData(privateKey ed25519.PrivateKey, slot ...ledger.Slot) *LedgerIdentityData {
	// creating origin 1 slot before now. More convenient for the workflow_old tests
	var sl ledger.Slot
	if len(slot) > 0 {
		sl = slot[0]
	} else {
		sl = ledger.TimeNow().Slot()
	}
	return &LedgerIdentityData{
		CoreLedgerConstraintsHash:          easyfl.LibraryHash(),
		Description:                        fmt.Sprintf("Proxima prototype version %s", global.Version),
		InitialSupply:                      DefaultInitialSupply,
		GenesisControllerPublicKey:         privateKey.Public().(ed25519.PublicKey),
		BaselineTime:                       ledger.BaselineTime,
		TimeTickDuration:                   ledger.TickDuration(),
		MaxTickValueInSlot:                 ledger.TicksPerSlot - 1,
		GenesisSlot:                        sl,
		SlotsPerLedgerYear:                 SlotsPerYear,
		InitialBranchBonus:                 InitialBranchInflationBonus,
		AnnualBranchBonusInflationPromille: AnnualBranchInflationPromille,
		ChainInflationPerTickFractionYoY: []uint64{
			ChainInflationFractionPerTick,
			ChainInflationFractionPerTick * 2,
			ChainInflationFractionPerTick * 4,
			ChainInflationFractionPerTick * 8,
			ChainInflationFractionPerTick * 14,
		},
	}
}
