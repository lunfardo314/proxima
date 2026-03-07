package ledger

import (
	"crypto/ed25519"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

type SigLock base.HolderID

const (
	SigLockName     = "a"
	sigLockTemplate = SigLockName + "(0x%s)"
)

//go:embed def/lock_signature.easyfl
var sigLockConstraintSource string

// SigLockFromBytes parses an SigLock using the provided library.
// Serde is library upgrade-independent
func SigLockFromBytes(data []byte) (ret SigLock, err error) {
	return SigLockFromBytesWithLib(data, L(base.MaxSlot))
}

func SigLockFromBytesWithLib(data []byte, lib *Library) (ret SigLock, err error) {
	var args [][]byte
	var sym string
	if sym, _, args, err = lib.ParseBytecodeOneLevel(data, 1); err != nil {
		err = fmt.Errorf("SigLockFromBytes: %v", err)
		return
	}
	if sym != SigLockName {
		err = fmt.Errorf("SigLockFromBytes: not a SigLock")
		return
	}
	addrBin := easyfl.StripDataPrefix(args[0])
	if len(addrBin) != 32 {
		err = fmt.Errorf("SigLockFromBytes: wrong data length")
		return
	}
	copy(ret[:], addrBin)
	return
}

func SigLockFromSource(src string) (SigLock, error) {
	bytecode, err := binFromSource(src)
	if err != nil {
		return SigLock{}, fmt.Errorf("SigLockFromSource: %v", err)
	}
	return SigLockFromBytes(bytecode)
}

func SigLockFromED25519PublicKey(pubKey ed25519.PublicKey) SigLock {
	return SigLock(base.HolderIDFromPublicKey(base.SignatureTypeED25519, pubKey))
}

func SigLockFromED25519PrivateKey(privateKey ed25519.PrivateKey) SigLock {
	return SigLockFromED25519PublicKey(privateKey.Public().(ed25519.PublicKey))
}

func SigLocksFromED25519PrivateKeys(privateKeys []ed25519.PrivateKey) []SigLock {
	ret := make([]SigLock, len(privateKeys))
	for i := range ret {
		ret[i] = SigLockFromED25519PrivateKey(privateKeys[i])
	}
	return ret
}

func SigLockMatchesED25519PrivateKey(addr SigLock, privateKey ed25519.PrivateKey) bool {
	return addr == SigLockFromED25519PrivateKey(privateKey)
}

func SigLockRandom() (ret SigLock) {
	_, err := rand.Read(ret[:])
	util.AssertNoError(err)
	return
}

func (a SigLock) Source() string {
	return fmt.Sprintf(sigLockTemplate, hex.EncodeToString(a[:]))
}

func (a SigLock) Bytes() []byte {
	return mustBinFromSource(a.Source())
}

func (a SigLock) Controllers() []Controller {
	return []Controller{a}
}

func (a SigLock) ControllerID() ControllerID {
	return a.Bytes()
}

func (a SigLock) Name() string {
	return SigLockName
}

func (a SigLock) String() string {
	return a.Source()
}

func (a SigLock) Short() string {
	return fmt.Sprintf(sigLockTemplate, hex.EncodeToString(a[:])[:8]+"..")
}

func (a SigLock) AsLock() Lock {
	return a
}

func (a SigLock) Master() Controller {
	return a
}

func registerAddressED25519Serde(lib *Library) {
	lib.mustRegisterConstraint(SigLockName, 1, func(data []byte) (Constraint, error) {
		return SigLockFromBytes(data)
	})
	lib.mustRegisterLockSerde(SigLockName, func(bytes []byte) (Lock, error) {
		ret, err := SigLockFromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		example := SigLock{}
		addrBack, err := SigLockFromBytes(example.Bytes())
		util.AssertNoError(err)
		util.Assertf(addrBack == SigLock{}, "inconsistency "+SigLockName)

		_, err = lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)
	})
}
