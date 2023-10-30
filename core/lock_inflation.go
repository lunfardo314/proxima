package core

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

type InflationLock struct {
	AddressED25519
}

const (
	InflationLockName     = "inflationLock"
	inflationLockTemplate = InflationLockName + "(0x%s)"
)

func InflationLockFromBytes(data []byte) (*InflationLock, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, err
	}
	if sym != InflationLockName {
		return nil, fmt.Errorf("not an InflationLock")
	}
	addrBin := easyfl.StripDataPrefix(args[0])
	if len(addrBin) != 32 {
		return nil, fmt.Errorf("wrong data length")
	}
	return &InflationLock{AddressED25519: addrBin}, nil
}

func InflationLockFromSource(src string) (*InflationLock, error) {
	bin, err := binFromSource(src)
	if err != nil {
		return nil, fmt.Errorf("EasyFL compile error: %v", err)
	}
	return InflationLockFromBytes(bin)
}

func InflationLockFromPrivateKey(privateKey ed25519.PrivateKey) *InflationLock {
	return &InflationLock{
		AddressED25519: AddressED25519FromPublicKey(privateKey.Public().(ed25519.PublicKey)),
	}
}

func (inf *InflationLock) source() string {
	return fmt.Sprintf(inflationLockTemplate, hex.EncodeToString(inf.AddressED25519))
}

func (inf *InflationLock) Bytes() []byte {
	return mustBinFromSource(inf.source())
}

func (inf *InflationLock) Clone() *InflationLock {
	return &InflationLock{
		AddressED25519: slices.Clone(inf.AddressED25519),
	}
}

func (inf *InflationLock) Accounts() []Accountable {
	return []Accountable{inf}
}

func (inf *InflationLock) UnlockableWith(acc AccountID, _ ...LogicalTime) bool {
	return bytes.Equal(inf.AccountID(), acc)
}

func (inf *InflationLock) AccountID() AccountID {
	return inf.Bytes()
}

func (inf *InflationLock) Name() string {
	return InflationLockName
}

func (inf *InflationLock) String() string {
	return inf.source()
}

func (inf *InflationLock) Short() string {
	return fmt.Sprintf(inflationLockTemplate, hex.EncodeToString(inf.AddressED25519)[:8]+"..")
}

func (inf *InflationLock) AsLock() Lock {
	return inf
}

func initInflationLockConstraint() {
	easyfl.MustExtendMany(InflationLockConstraintSource)

	example := &InflationLock{AddressED25519Null()}
	inflationLockBack, err := InflationLockFromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(inflationLockBack, example), "inconsistency "+InflationLockName)

	prefix, err := easyfl.ParseBytecodePrefix(example.Bytes())
	util.AssertNoError(err)

	registerConstraint(InflationLockName, prefix, func(data []byte) (Constraint, error) {
		return InflationLockFromBytes(data)
	})
}

const InflationLockConstraintSource = `
// $0 - ED25519 address, 32 byte blake2b hash of the public key
func inflationLock: and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1),
    require(addressED25519($0), !!!inflationLock_unlock_failed)
)
`
