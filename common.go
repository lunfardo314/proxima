package proxima

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lazyslice"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
)

type (
	StateReader interface {
		GetUTXO(id *core.OutputID) ([]byte, bool)
		HasUTXO(id *core.OutputID) bool
	}

	StateIndexReader interface {
		GetUTXOsLockedInAccount(accountID core.AccountID) ([]*core.OutputDataWithID, error)
		GetUTXOForChainID(id *core.ChainID) (*core.OutputDataWithID, error)
		Root() common.VCommitment
		IdentityData() *StateIdentityData
	}

	// IndexedStateReader state and indexer readers packing together
	IndexedStateReader interface {
		StateReader
		StateIndexReader
	}

	StateStore interface {
		common.KVReader
		common.BatchedUpdatable
		common.Traversable
	}

	StateIdentityData struct {
		Description              string
		InitialSupply            uint64
		GenesisControllerAddress core.AddressED25519
		GenesisEpoch             core.TimeSlot
	}
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
		id.GenesisEpoch.Bytes(),
	).Bytes()
}

func (id *StateIdentityData) OriginChainID() core.ChainID {
	oid := GenesisChainOutputID(id.GenesisEpoch)
	return core.OriginChainID(&oid)
}

func (id *StateIdentityData) String() string {
	originChainID := id.OriginChainID()
	return fmt.Sprintf("Description: '%s'\nInitial supply: %s\nController: %s\nGenesis time slot: %d\nOrigin chainID: %s",
		id.Description, testutil.GoThousands(id.InitialSupply), id.GenesisControllerAddress.String(), id.GenesisEpoch, originChainID.String())
}

func MustIdentityDataFromBytes(data []byte) *StateIdentityData {
	arr, err := lazyslice.ParseArrayFromBytesReadOnly(data, 4)
	util.AssertNoError(err)
	return &StateIdentityData{
		Description:              string(arr.At(0)),
		InitialSupply:            binary.BigEndian.Uint64(arr.At(1)),
		GenesisControllerAddress: core.MustAddressED25519FromBytes(arr.At(2)),
		GenesisEpoch:             core.MustTimeSlotFromBytes(arr.At(3)),
	}
}

func GenesisTransactionID(genesisEpoch core.TimeSlot) *core.TransactionID {
	ret := core.NewTransactionID(core.MustNewLogicalTime(genesisEpoch, 0), core.All0TransactionHash, true, true)
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
