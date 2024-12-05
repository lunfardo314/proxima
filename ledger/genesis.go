package ledger

import (
	"encoding/hex"

	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

const (
	BootstrapSequencerName = "boot"
	// BoostrapSequencerIDHex is a constant
	BoostrapSequencerIDHex = "6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc"
)

// BoostrapSequencerID is a constant
var BoostrapSequencerID ChainID

// init BoostrapSequencerID constant and check consistency

func init() {
	data, err := hex.DecodeString(BoostrapSequencerIDHex)
	util.AssertNoError(err)
	BoostrapSequencerID, err = ChainIDFromBytes(data)
	util.AssertNoError(err)
	// calculate directly and check
	var zero33 [33]byte
	zero33[0] = 0b10000000
	genesisOutputID := GenesisOutputID()
	bootSeqIDDirect := blake2b.Sum256(genesisOutputID[:])
	util.Assertf(BoostrapSequencerID == bootSeqIDDirect, "BoostrapSequencerID must be equal to the blake2b hash of genesis output ID, got %s", hex.EncodeToString(bootSeqIDDirect[:]))
	// more checks
	oid := GenesisOutputID()
	util.Assertf(MakeOriginChainID(&oid) == BoostrapSequencerID, "MakeOriginChainID(&oid) == BoostrapSequencerID")
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
