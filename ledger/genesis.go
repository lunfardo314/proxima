package ledger

import (
	"encoding/hex"

	"github.com/lunfardo314/proxima/util"
)

const (
	BootstrapSequencerName = "boot"
	BoostrapSequencerIDHex = "3053dcd151cf11713e16644d0746a9dc4c22fe8ea11c6260f3fee695720dde3c"
)

var BoostrapSequencerID ChainID

func init() {
	oid := GenesisOutputID()
	id := MakeOriginChainID(&oid)
	data, err := hex.DecodeString(BoostrapSequencerIDHex)
	util.AssertNoError(err)
	BoostrapSequencerID, err = ChainIDFromBytes(data)
	util.AssertNoError(err)
	util.Assertf(BoostrapSequencerID == id, "inconsistency: bootstrap sequencer ID ")
}

func GenesisOutput(initialSupply uint64, controllerAddress AddressED25519) *OutputWithChainID {
	oid := GenesisOutputID()
	return &OutputWithChainID{
		OutputWithID: OutputWithID{
			ID: oid,
			Output: NewOutput(func(o *Output) {
				o.WithAmount(initialSupply).WithLock(controllerAddress)
				chainIdx, err := o.PushConstraint(NewChainOrigin().Bytes())
				util.AssertNoError(err)
				_, err = o.PushConstraint(NewSequencerConstraint(chainIdx, initialSupply).Bytes())
				util.AssertNoError(err)

				msData := MilestoneData{Name: BootstrapSequencerName}
				idxMsData, err := o.PushConstraint(msData.AsConstraint().Bytes())
				util.AssertNoError(err)
				util.Assertf(idxMsData == MilestoneDataFixedIndex, "idxMsData == MilestoneDataFixedIndex")
			}),
		},
		ChainID: BoostrapSequencerID,
	}
}

func GenesisStemOutput() *OutputWithID {
	return &OutputWithID{
		ID: GenesisStemOutputID(),
		Output: NewOutput(func(o *Output) {
			o.WithAmount(0).
				WithLock(&StemLock{
					PredecessorOutputID: OutputID{},
				})
		}),
	}
}
