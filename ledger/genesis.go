package ledger

import (
	"encoding/hex"

	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
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

	// calculate directly and check
	var zero33 [33]byte
	zero33[0] = 0b11000000
	util.Assertf(BoostrapSequencerID == blake2b.Sum256(zero33[:]), "BoostrapSequencerID must be equal to the blake2b hash of 33-long zero bytes array with first 2 bits sets to 1")
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
