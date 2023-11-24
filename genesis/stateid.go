package genesis

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazybytes"
	"github.com/lunfardo314/proxima/util/lines"
	"gopkg.in/yaml.v2"
)

// StateIdentityData is provided at genesis and will remain immutable during lifetime
// All integers are serialized as big-endian
type (
	StateIdentityData struct {
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
		GenesisTimeSlot core.TimeSlot
		// core constraint library hash. For checking of ledger version compatibility with the node
		CoreLibraryHash [32]byte
	}

	// stateIdentityDataYAMLable structure for canonic yamlAble marshaling
	stateIdentityDataYAMLable struct {
		Description                string `yaml:"description"`
		InitialSupply              uint64 `yaml:"initialSupply"`
		GenesisControllerPublicKey string `yaml:"genesisControllerPublicKey"`
		BaselineTime               int64  `yaml:"baselineTime"`
		TimeTickDuration           int64  `yaml:"timeTickDuration"`
		MaxTimeTickValueInTimeSlot uint8  `yaml:"maxTimeTickValueInTimeSlot"`
		GenesisTimeSlot            uint32 `yaml:"genesisTimeSlot"`
		CoreLibraryHash            string `yaml:"coreLibraryHash"`
	}
)

const (
	InitialSupplyOutputIndex = byte(0)
	StemOutputIndex          = byte(1)
)

func (id *StateIdentityData) Bytes() []byte {
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
		[]byte(id.Description),        // 0
		supplyBin[:],                  // 1
		id.GenesisControllerPublicKey, // 2
		baselineTimeBin[:],            // 3
		timeTickDurationBin[:],        // 4
		maxTickBin[:],                 // 5
		genesisTimesSlotBin[:],        // 6
		id.CoreLibraryHash[:],         // 7
	).Bytes()
}

func MustStateIdentityDataFromBytes(data []byte) *StateIdentityData {
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
	util.Assertf(baselineTime.UnixNano() == core.BaselineTimeUnixNano, msg, core.BaselineTimeUnixNano, baselineTime)

	// check time tick duration
	timeTickDuration := time.Duration(binary.BigEndian.Uint64(arr.At(4)))
	msg = "node assumes time tick duration different from state baseline duration: expected %dns, got %dns"
	util.Assertf(timeTickDuration == core.TimeTickDuration(), msg, core.TimeTickDuration().Nanoseconds(), timeTickDuration.Nanoseconds())

	// check time ticks per slot
	msg = "node assumes time ticks per slot different from state assumption: expected %d, got %d"
	util.Assertf(maxTick[0]+1 == core.TimeTicksPerSlot, msg, core.TimeTicksPerSlot, maxTick[0]+1)

	ret := &StateIdentityData{
		Description:                string(arr.At(0)),
		InitialSupply:              binary.BigEndian.Uint64(arr.At(1)),
		GenesisControllerPublicKey: publicKey,
		BaselineTime:               baselineTime,
		TimeTickDuration:           timeTickDuration,
		MaxTimeTickValueInTimeSlot: maxTick[0],
		GenesisTimeSlot:            core.MustTimeSlotFromBytes(arr.At(6)),
	}
	copy(ret.CoreLibraryHash[:], arr.At(7))
	return ret
}

func (id *StateIdentityData) GenesisControlledAddress() core.AddressED25519 {
	return core.AddressED25519FromPublicKey(id.GenesisControllerPublicKey)
}

func (id *StateIdentityData) TimeTicksPerTimeSlot() int {
	return int(id.MaxTimeTickValueInTimeSlot) + 1
}

func (id *StateIdentityData) OriginChainID() core.ChainID {
	oid := InitialSupplyOutputID(id.GenesisTimeSlot)
	return core.OriginChainID(&oid)
}

func (id *StateIdentityData) String() string {
	return id.Lines().String()
}

func (id *StateIdentityData) Lines(prefix ...string) *lines.Lines {
	originChainID := id.OriginChainID()
	initialSupplyOutputID := InitialSupplyOutputID(id.GenesisTimeSlot)
	genesisStemOutputID := StemOutputID(id.GenesisTimeSlot)
	return lines.New(prefix...).
		Add("Description: '%s'", id.Description).
		Add("Constraint library hash: %s", hex.EncodeToString(id.CoreLibraryHash[:])).
		Add("Initial supply: %s", util.GoThousands(id.InitialSupply)).
		Add("Genesis controller address: %s", id.GenesisControlledAddress().String()).
		Add("Baseline time: %s", id.BaselineTime.Format(time.RFC3339)).
		Add("Time tick duration: %v", id.TimeTickDuration).
		Add("Time ticks per time slot: %d", id.TimeTicksPerTimeSlot()).
		Add("Genesis time slot: %d", id.GenesisTimeSlot).
		Add("Origin chain ID: %s", originChainID.String()).
		Add("Initial supply output ID: %s", initialSupplyOutputID.String()).
		Add("Genesis stem output ID: %s", genesisStemOutputID.String())
}

func (id *StateIdentityData) yamlAble() *stateIdentityDataYAMLable {
	return &stateIdentityDataYAMLable{
		Description:                id.Description,
		InitialSupply:              id.InitialSupply,
		GenesisControllerPublicKey: hex.EncodeToString(id.GenesisControllerPublicKey),
		BaselineTime:               id.BaselineTime.UnixNano(),
		TimeTickDuration:           id.TimeTickDuration.Nanoseconds(),
		MaxTimeTickValueInTimeSlot: id.MaxTimeTickValueInTimeSlot,
		GenesisTimeSlot:            uint32(id.GenesisTimeSlot),
		CoreLibraryHash:            hex.EncodeToString(id.CoreLibraryHash[:]),
	}
}

func (id *StateIdentityData) YAML() []byte {
	return id.yamlAble().YAML()
}

const stateIDComment = `# This file contains Proxima ledger identity data.
# It will be used to create genesis ledger state for the Proxima network.
# The ledger identity file does not contain secrets, it is public.
# The data in the file must match genesis controller private key and hardcoded protocol constants.
# Except 'description' field, file should not be modified.
# Once used to create genesis, identity data should never be modified.
`

func (id *stateIdentityDataYAMLable) YAML() []byte {
	var buf bytes.Buffer
	data, err := yaml.Marshal(id)
	buf.WriteString(stateIDComment)
	buf.Write(data)
	util.AssertNoError(err)
	return buf.Bytes()
}

func (id *stateIdentityDataYAMLable) stateIdentityData() (*StateIdentityData, error) {
	var err error
	ret := &StateIdentityData{}
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
	ret.GenesisTimeSlot = core.TimeSlot(id.GenesisTimeSlot)
	hBin, err := hex.DecodeString(id.CoreLibraryHash)
	if err != nil {
		return nil, err
	}
	if len(hBin) != 32 {
		return nil, fmt.Errorf("wrong core library hash")
	}
	copy(ret.CoreLibraryHash[:], hBin)
	return ret, nil
}

func StateIdentityDataFromYAML(yamlData []byte) (*StateIdentityData, error) {
	yamlAble := &stateIdentityDataYAMLable{}
	if err := yaml.Unmarshal(yamlData, &yamlAble); err != nil {
		return nil, err
	}
	return yamlAble.stateIdentityData()
}

const DefaultSupply = 1_000_000_000_000

func DefaultIdentityData(privateKey ed25519.PrivateKey, slot ...core.TimeSlot) *StateIdentityData {
	// creating origin 1 slot before now. More convenient for the workflow tests
	var sl core.TimeSlot
	if len(slot) > 0 {
		sl = slot[0]
	} else {
		sl = core.LogicalTimeNow().TimeSlot()
	}
	return &StateIdentityData{
		CoreLibraryHash:            easyfl.LibraryHash(),
		Description:                fmt.Sprintf("Proxima prototype version %s", general.Version),
		InitialSupply:              DefaultSupply,
		GenesisControllerPublicKey: privateKey.Public().(ed25519.PublicKey),
		BaselineTime:               core.BaselineTime,
		TimeTickDuration:           core.TimeTickDuration(),
		MaxTimeTickValueInTimeSlot: core.TimeTicksPerSlot - 1,
		GenesisTimeSlot:            sl,
	}
}
