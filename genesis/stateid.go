package genesis

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazyslice"
	"github.com/lunfardo314/proxima/util/lines"
)

// IdentityData is provided at genesis and will remain immutable during lifetime
// All integers are serialized as big-endian
type IdentityData struct {
	// arbitrary string up 255 bytes
	Description string
	// initial supply of tokens
	InitialSupply uint64
	// blake2b hash of the ED25519 public key, interpreted as address
	GenesisControllerPublicKey ed25519.PublicKey
	// baseline time unix nanoseconds, big-endian
	BaselineTime time.Time
	// time tick duration in nanoseconds
	TimeTickDuration time.Duration
	// max time tick value in the slot. Up to 256 time ticks per time slot
	MaxTimeTickValueInTimeSlot uint8
	// time slot of the genesis
	GenesisTimeSlot core.TimeSlot
}

const (
	InitialSupplyOutputIndex = byte(0)
	StemOutputIndex          = byte(1)
)

func (id *IdentityData) Bytes() []byte {
	var supplyBin [8]byte
	binary.BigEndian.PutUint64(supplyBin[:], id.InitialSupply)
	var baselineTimeBin [8]byte
	binary.BigEndian.PutUint64(baselineTimeBin[:], uint64(id.BaselineTime.UnixNano()))
	var timeTickDurationBin [8]byte
	binary.BigEndian.PutUint64(timeTickDurationBin[:], uint64(id.TimeTickDuration.Nanoseconds()))
	maxTickBin := []byte{id.MaxTimeTickValueInTimeSlot}
	var genesisTimesSlotBin [4]byte
	binary.BigEndian.PutUint32(genesisTimesSlotBin[:], uint32(id.GenesisTimeSlot))

	return lazyslice.MakeArrayFromDataReadOnly(
		[]byte(id.Description),        // 0
		supplyBin[:],                  // 1
		id.GenesisControllerPublicKey, // 2
		baselineTimeBin[:],            // 3
		timeTickDurationBin[:],        // 4
		maxTickBin[:],                 // 5
		genesisTimesSlotBin[:],        // 6
	).Bytes()
}

func MustIdentityDataFromBytes(data []byte) *IdentityData {
	arr, err := lazyslice.ParseArrayFromBytesReadOnly(data, 7)
	util.AssertNoError(err)
	publicKey := ed25519.PublicKey(arr.At(2))
	util.Assertf(len(publicKey) == ed25519.PublicKeySize, "len(publicKey)==ed25519.PublicKeySize")
	maxTick := arr.At(5)
	util.Assertf(len(maxTick) == 1, "len(maxTick)==1")
	return &IdentityData{
		Description:                string(arr.At(0)),
		InitialSupply:              binary.BigEndian.Uint64(arr.At(1)),
		GenesisControllerPublicKey: publicKey,
		BaselineTime:               time.Unix(0, int64(binary.BigEndian.Uint64(arr.At(3)))),
		TimeTickDuration:           time.Duration(binary.BigEndian.Uint64(arr.At(4))),
		MaxTimeTickValueInTimeSlot: maxTick[0],
		GenesisTimeSlot:            core.MustTimeSlotFromBytes(arr.At(6)),
	}
}

func (id *IdentityData) GenesisControlledAddress() core.AddressED25519 {
	return core.AddressED25519FromPublicKey(id.GenesisControllerPublicKey)
}

func (id *IdentityData) TimeTicksPerTimeSlot() int {
	return int(id.MaxTimeTickValueInTimeSlot) + 1
}

func (id *IdentityData) OriginChainID() core.ChainID {
	oid := InitialSupplyOutputID(id.GenesisTimeSlot)
	return core.OriginChainID(&oid)
}

func (id *IdentityData) String() string {
	originChainID := id.OriginChainID()
	initialSupplyOutputID := InitialSupplyOutputID(id.GenesisTimeSlot)
	genesisStemOutputID := StemOutputID(id.GenesisTimeSlot)
	return lines.New().
		Add("Description: '%s'", id.Description).
		Add("Initial supply: %s", util.GoThousands(id.InitialSupply)).
		Add("Genesis controller address: %s", id.GenesisControlledAddress().String()).
		Add("Baseline time: %s", id.BaselineTime.Format(time.RFC3339)).
		Add("Time tick duration: %v", id.TimeTickDuration).
		Add("Time ticks per time slot: %d", id.TimeTicksPerTimeSlot()).
		Add("Genesis time slot: %d", id.GenesisTimeSlot).
		Add("Origin chain ID: %s", originChainID.String()).
		Add("Initial supply output ID: %s", initialSupplyOutputID.String()).
		Add("Genesis stem output ID: %s", genesisStemOutputID.String()).
		String()
}

const DefaultSupply = 1_000_000_000_000

func DefaultIdentityData(privateKey ed25519.PrivateKey, slot ...core.TimeSlot) *IdentityData {
	// creating origin 1 slot before now. More convenient for the workflow tests
	var sl core.TimeSlot
	if len(slot) > 0 {
		sl = slot[0]
	} else {
		sl = core.LogicalTimeNow().TimeSlot()
	}
	return &IdentityData{
		Description:                fmt.Sprintf("Proxima prototype version %s", general.Version),
		InitialSupply:              DefaultSupply,
		GenesisControllerPublicKey: privateKey.Public().(ed25519.PublicKey),
		BaselineTime:               core.BaselineTime,
		TimeTickDuration:           core.TimeTickDuration(),
		MaxTimeTickValueInTimeSlot: core.TimeTicksPerSlot - 1,
		GenesisTimeSlot:            sl,
	}
}
