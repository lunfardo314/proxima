package ledger

import (
	"crypto/ed25519"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// SigLock is a typed wrapper for the 32-byte ed25519 holder ID. The output
// element at index 1 (index-value tuple) of a sig-locked output is
// (holderID); the bytecode at index 2 is the per-kind constant
// `sigLock` 0-arg call (sigLockBytecode).
type SigLock base.HolderID

const SigLockName = "sigLock"

//go:embed def/lock_signature.easyfl
var sigLockConstraintSource string

// sigLockBytecode returns the bytecode of the public 0-arg `sigLock`
// constraint that occupies output element index 2 of every sig-locked
// output. The value is the same for all sig-locked outputs (the holder
// data lives in the index-value tuple at index 1).
var (
	sigLockBytecodeOnce  sync.Once
	sigLockBytecodeCache []byte
)

func SigLockBytecode() []byte {
	sigLockBytecodeOnce.Do(func() {
		sigLockBytecodeCache = mustBinFromSource(SigLockName)
	})
	return sigLockBytecodeCache
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

// String returns a human-readable representation of the sigLock holder.
func (a SigLock) String() string {
	return fmt.Sprintf("sigLock(0x%s)", hex.EncodeToString(a[:]))
}

func (a SigLock) Short() string {
	return fmt.Sprintf("sigLock(0x%s..)", hex.EncodeToString(a[:])[:8])
}

// IndexValues returns the single 32-byte holder ID — the index-value
// tuple of a sig-locked output is (holderID).
func (a SigLock) IndexValues() [][]byte {
	return [][]byte{a[:]}
}

func (a SigLock) Name() string               { return SigLockName }
func (a SigLock) LockBytecode() []byte       { return SigLockBytecode() }
func (a SigLock) ControllerID() ControllerID { return a[:] }

// Source returns the wallet/CLI mini-syntax `sigLock/<64-hex>`.
func (a SigLock) Source() string {
	return SigLockName + "/" + hex.EncodeToString(a[:])
}

// SigLockFromSource parses the wallet/CLI mini-syntax `sigLock/<64-hex>`
// into a SigLock. Replaces the previous EasyFL-source compilation path.
func SigLockFromSource(src string) (SigLock, error) {
	id, kind, err := ControllerIDFromSource(src)
	if err != nil {
		return SigLock{}, err
	}
	if kind != SigLockName {
		return SigLock{}, fmt.Errorf("SigLockFromSource: expected sigLock kind, got %s", kind)
	}
	var ret SigLock
	copy(ret[:], id)
	return ret, nil
}

func registerAddressED25519Serde(lib *Library) {
	lib.registerLockKind(SigLockName)
}
