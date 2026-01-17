package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	_ "embed"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

const (
	StemLockName = "stemLock"
	stemTemplate = StemLockName + "(0x%s,0x%s)"
)

type (
	StemLock struct {
		PredecessorOutputID base.OutputID
		VRFProof            []byte
	}
)

//go:embed lock_stem.efl
var stemLockSource string

var StemAccountID = AccountID([]byte{0})

func (st *StemLock) AccountID() AccountID {
	return StemAccountID
}

func (st *StemLock) AsLock() Lock {
	return st
}

func (st *StemLock) Name() string {
	return StemLockName
}

func (st *StemLock) Source() string {
	return fmt.Sprintf(stemTemplate,
		hex.EncodeToString(st.PredecessorOutputID[:]),
		hex.EncodeToString(st.VRFProof),
	)
}

func (st *StemLock) Bytes() []byte {
	return mustBinFromSource(st.Source())
}

func (st *StemLock) String() string {
	return st.Source()
	//return fmt.Sprintf("stem(%s)", st.PredecessorOutputID.StringShort())
}

func (st *StemLock) Accounts() []Accountable {
	return []Accountable{st}
}

func (st *StemLock) Master() Accountable {
	return nil
}

func registerStemLockConstraint(lib *Library) {
	lib.mustRegisterConstraint(StemLockName, 2, func(data []byte) (Constraint, error) {
		// Use latest library version for library registration parsing
		return StemLockFromBytesWithLib(data, lib)
	})
	lib.mustRegisterLockSerde(StemLockName, func(bytes []byte) (Lock, error) {
		// Use latest library version for library registration parsing
		ret, err := StemLockFromBytesWithLib(bytes, lib)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		txid := base.RandomTransactionID(true, 1)
		predID := base.MustNewOutputID(txid, byte(txid.NumProducedOutputs()-1))
		example := StemLock{
			PredecessorOutputID: predID,
			VRFProof:            []byte{0x01, 0x02, 0x03},
		}
		exampleBack, err := StemLockFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(bytes.Equal(example.Bytes(), exampleBack.Bytes()), "bytes.Equal(example.Bytes(), exampleBack.Bytes())")
		_, err = lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)
	})
}

// StemLockFromBytesWithLib parses a StemLock using the provided library.
// This is the core implementation that avoids repeated L(slot) calls.
func StemLockFromBytesWithLib(data []byte, lib *Library) (*StemLock, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, err
	}
	if sym != StemLockName {
		return nil, fmt.Errorf("not a 'stem' constraint")
	}
	oid, err := base.OutputIDFromBytes(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, err
	}

	return &StemLock{
		PredecessorOutputID: oid,
		VRFProof:            easyfl.StripDataPrefix(args[1]),
	}, nil
}
