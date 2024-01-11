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
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/proxima/util/lines"
	"golang.org/x/crypto/blake2b"
	"gopkg.in/yaml.v2"
)

// LedgerIdentityData is provided at genesis and will remain immutable during lifetime
// All integers are serialized as big-endian
type (
	LedgerIdentityData struct {
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
		// max time tick value in the slot. Up to 256 time ticks per time slot
		MaxTimeTickValueInTimeSlot uint8
		// time slot of the genesis
		GenesisTimeSlot ledger.TimeSlot
		// core constraint library hash. For checking of ledger version compatibility with the node
		CoreLedgerConstraintsHash [32]byte
	}

	// ledgerIdentityDataYAMLable structure for canonic yamlAble marshaling
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
	var supplyBin [8]byte
	binary.BigEndian.PutUint64(supplyBin[:], id.InitialSupply)
	var baselineTimeBin [8]byte
	binary.BigEndian.PutUint64(baselineTimeBin[:], uint64(id.BaselineTime.UnixNano()))
	var timeTickDurationBin [8]byte
	binary.BigEndian.PutUint64(timeTickDurationBin[:], uint64(id.TimeTickDuration.Nanoseconds()))
	maxTickBin := []byte{id.MaxTimeTickValueInTimeSlot}
	var genesisTimesSlotBin [4]byte
	binary.BigEndian.PutUint32(genesisTimesSlotBin[:], uint32(id.GenesisTimeSlot))

	return lazybytes.MakeArrayFromDataReadOnly(
		[]byte(id.Description),          // 0
		supplyBin[:],                    // 1
		id.GenesisControllerPublicKey,   // 2
		baselineTimeBin[:],              // 3
		timeTickDurationBin[:],          // 4
		maxTickBin[:],                   // 5
		genesisTimesSlotBin[:],          // 6
		id.CoreLedgerConstraintsHash[:], // 7
	).Bytes()
}

func (id *LedgerIdentityData) Hash() [32]byte {
	return blake2b.Sum256(id.Bytes())
}

func MustLedgerIdentityDataFromBytes(data []byte) *LedgerIdentityData {
	arr, err := lazybytes.ParseArrayFromBytesReadOnly(data, 8)
	util.AssertNoError(err)
	publicKey := ed25519.PublicKey(arr.At(2))
	util.Assertf(len(publicKey) == ed25519.PublicKeySize, "len(publicKey)==ed25519.PublicKeySize")
	maxTick := arr.At(5)
	util.Assertf(len(maxTick) == 1, "len(maxTick)==1")

	// check library hashes
	libraryHash := easyfl.LibraryHash()
	msg := "node's constraint library is incompatible with the multi-state identity\nExpected library hash %s, got %s"
	util.Assertf(bytes.Equal(libraryHash[:], arr.At(7)), msg, hex.EncodeToString(libraryHash[:]), hex.EncodeToString(arr.At(7)))

	// check baseline time
	baselineTime := time.Unix(0, int64(binary.BigEndian.Uint64(arr.At(3))))
	msg = "node assumes baseline time different from state baseline time: expected %v, got %v"
	util.Assertf(baselineTime.UnixNano() == ledger.BaselineTimeUnixNano, msg, ledger.BaselineTimeUnixNano, baselineTime)

	// check time tick duration
	timeTickDuration := time.Duration(binary.BigEndian.Uint64(arr.At(4)))
	msg = "node assumes time tick duration different from state baseline duration: expected %dns, got %dns"
	util.Assertf(timeTickDuration == ledger.TimeTickDuration(), msg, ledger.TimeTickDuration().Nanoseconds(), timeTickDuration.Nanoseconds())

	// check time ticks per slot
	msg = "node assumes time ticks per slot different from state assumption: expected %d, got %d"
	util.Assertf(maxTick[0]+1 == ledger.TimeTicksPerSlot, msg, ledger.TimeTicksPerSlot, maxTick[0]+1)

	ret := &LedgerIdentityData{
		Description:                string(arr.At(0)),
		InitialSupply:              binary.BigEndian.Uint64(arr.At(1)),
		GenesisControllerPublicKey: publicKey,
		BaselineTime:               baselineTime,
		TimeTickDuration:           timeTickDuration,
		MaxTimeTickValueInTimeSlot: maxTick[0],
		GenesisTimeSlot:            ledger.MustTimeSlotFromBytes(arr.At(6)),
	}
	copy(ret.CoreLedgerConstraintsHash[:], arr.At(7))
	return ret
}

func (id *LedgerIdentityData) GenesisControlledAddress() ledger.AddressED25519 {
	return ledger.AddressED25519FromPublicKey(id.GenesisControllerPublicKey)
}

func (id *LedgerIdentityData) TimeTicksPerTimeSlot() int {
	return int(id.MaxTimeTickValueInTimeSlot) + 1
}

func (id *LedgerIdentityData) OriginChainID() ledger.ChainID {
	oid := InitialSupplyOutputID(id.GenesisTimeSlot)
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
		Add("Initial supply: %s", util.GoThousands(id.InitialSupply)).
		Add("Genesis controller address: %s", id.GenesisControlledAddress().String()).
		Add("Baseline time: %s", id.BaselineTime.Format(time.RFC3339)).
		Add("Time tick duration: %v", id.TimeTickDuration).
		Add("Time ticks per time slot: %d", id.TimeTicksPerTimeSlot()).
		Add("Genesis time slot: %d", id.GenesisTimeSlot).
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
		MaxTimeTickValueInTimeSlot: id.MaxTimeTickValueInTimeSlot,
		GenesisTimeSlot:            uint32(id.GenesisTimeSlot),
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
	ret.MaxTimeTickValueInTimeSlot = id.MaxTimeTickValueInTimeSlot
	ret.GenesisTimeSlot = ledger.TimeSlot(id.GenesisTimeSlot)
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

const DefaultSupply = 1_000_000_000_000

func DefaultIdentityData(privateKey ed25519.PrivateKey, slot ...ledger.TimeSlot) *LedgerIdentityData {
	// creating origin 1 slot before now. More convenient for the workflow_old tests
	var sl ledger.TimeSlot
	if len(slot) > 0 {
		sl = slot[0]
	} else {
		sl = ledger.LogicalTimeNow().TimeSlot()
	}
	return &LedgerIdentityData{
		CoreLedgerConstraintsHash:  easyfl.LibraryHash(),
		Description:                fmt.Sprintf("Proxima prototype version %s", global.Version),
		InitialSupply:              DefaultSupply,
		GenesisControllerPublicKey: privateKey.Public().(ed25519.PublicKey),
		BaselineTime:               ledger.BaselineTime,
		TimeTickDuration:           ledger.TimeTickDuration(),
		MaxTimeTickValueInTimeSlot: ledger.TimeTicksPerSlot - 1,
		GenesisTimeSlot:            sl,
	}
}
