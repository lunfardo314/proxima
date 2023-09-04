package general

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazyslice"
)

const (
	GenesisOutputIndex     = byte(0)
	GenesisStemOutputIndex = byte(1)
)

func (id *StateIdentityData) Bytes() []byte {
	var supplyBin [8]byte
	binary.BigEndian.PutUint64(supplyBin[:], id.InitialSupply)
	return lazyslice.MakeArrayFromDataReadOnly(
		[]byte(id.Description),
		supplyBin[:],
		id.GenesisControllerAddress.Bytes(),
		id.GenesisTimeSlot.Bytes(),
	).Bytes()
}

func (id *StateIdentityData) OriginChainID() core.ChainID {
	oid := GenesisChainOutputID(id.GenesisTimeSlot)
	return core.OriginChainID(&oid)
}

func (id *StateIdentityData) String() string {
	originChainID := id.OriginChainID()
	return fmt.Sprintf("Description: '%s'\nInitial supply: %s\nController: %s\nGenesis time slot: %d\nOrigin chainID: %s",
		id.Description, util.GoThousands(id.InitialSupply), id.GenesisControllerAddress.String(), id.GenesisTimeSlot, originChainID.String())
}

func MustIdentityDataFromBytes(data []byte) *StateIdentityData {
	arr, err := lazyslice.ParseArrayFromBytesReadOnly(data, 4)
	util.AssertNoError(err)
	return &StateIdentityData{
		Description:              string(arr.At(0)),
		InitialSupply:            binary.BigEndian.Uint64(arr.At(1)),
		GenesisControllerAddress: core.MustAddressED25519FromBytes(arr.At(2)),
		GenesisTimeSlot:          core.MustTimeSlotFromBytes(arr.At(3)),
	}
}

func GenesisTransactionID(genesisTimeSlot core.TimeSlot) *core.TransactionID {
	ret := core.NewTransactionID(core.MustNewLogicalTime(genesisTimeSlot, 0), core.All0TransactionHash, true, true)
	return &ret
}

func GenesisChainOutputID(e core.TimeSlot) (ret core.OutputID) {
	// we are placing sequencer flag = true into the genesis tx ID to please sequencer constraint
	// of the origin branch transaction. It is the only exception
	ret = core.NewOutputID(GenesisTransactionID(e), GenesisOutputIndex)
	return
}

func GenesisStemOutputID(e core.TimeSlot) (ret core.OutputID) {
	ret = core.NewOutputID(GenesisTransactionID(e), GenesisStemOutputIndex)
	return
}
