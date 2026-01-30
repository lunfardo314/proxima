package ledger

import (
	"crypto/ed25519"
	_ "embed"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

type AddressED25519 []byte

const (
	AddressED25519Name     = "a"
	addressED25519Template = AddressED25519Name + "(0x%s)"
)

//go:embed def/lock_ed25519.easyfl
var addressED25519ConstraintSource string

// AddressED25519FromBytes parses an AddressED25519 using the provided library.
// Serde is library upgrade-independent
func AddressED25519FromBytes(data []byte) (AddressED25519, error) {
	sym, _, args, err := L(base.MaxSlot).ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, err
	}
	if sym != AddressED25519Name {
		return nil, fmt.Errorf("AddressED25519FromSource: not an AddressED25519")
	}
	addrBin := easyfl.StripDataPrefix(args[0])
	if len(addrBin) != 32 {
		return nil, fmt.Errorf("AddressED25519FromSource: wrong data length")
	}
	return addrBin, nil
}

func AddressED25519FromSource(src string) (AddressED25519, error) {
	bin, err := binFromSource(src)
	if err != nil {
		return nil, fmt.Errorf("AddressED25519FromSource: EasyFL compile error: %v", err)
	}
	return AddressED25519FromBytes(bin)
}

func AddressED25519FromPublicKey(pubKey ed25519.PublicKey) AddressED25519 {
	h := blake2b.Sum256(pubKey)
	return h[:]
}

func AddressED25519FromPrivateKey(privateKey ed25519.PrivateKey) AddressED25519 {
	return AddressED25519FromPublicKey(privateKey.Public().(ed25519.PublicKey))
}

func AddressesED25519FromPrivateKeys(privateKeys []ed25519.PrivateKey) []AddressED25519 {
	ret := make([]AddressED25519, len(privateKeys))
	for i := range ret {
		ret[i] = AddressED25519FromPrivateKey(privateKeys[i])
	}
	return ret
}

func AddressED25519MatchesPrivateKey(addr AddressED25519, privateKey ed25519.PrivateKey) bool {
	return EqualConstraints(AddressED25519FromPrivateKey(privateKey), addr)
}

func AddressED25519Null() AddressED25519 {
	return make([]byte, 32)
}

func AddressED25519Random() AddressED25519 {
	_, priv, err := ed25519.GenerateKey(nil)
	util.AssertNoError(err)
	return AddressED25519FromPrivateKey(priv)
}

func (a AddressED25519) Source() string {
	return fmt.Sprintf(addressED25519Template, hex.EncodeToString(a))
}

func (a AddressED25519) Bytes() []byte {
	return mustBinFromSource(a.Source())
}

func (a AddressED25519) Clone() AddressED25519 {
	return slices.Clone(a)
}

func (a AddressED25519) Accounts() []Accountable {
	return []Accountable{a}
}

func (a AddressED25519) AccountID() AccountID {
	return a.Bytes()
}

func (a AddressED25519) Name() string {
	return AddressED25519Name
}

func (a AddressED25519) String() string {
	return a.Source()
}

func (a AddressED25519) Short() string {
	return fmt.Sprintf(addressED25519Template, hex.EncodeToString(a)[:8]+"..")
}

func (a AddressED25519) AsLock() Lock {
	return a
}

func (a AddressED25519) Master() Accountable {
	return a
}

func registerAddressED25519Serde(lib *Library) {
	lib.mustRegisterConstraint(AddressED25519Name, 1, func(data []byte) (Constraint, error) {
		return AddressED25519FromBytes(data)
	})
	lib.mustRegisterLockSerde(AddressED25519Name, func(bytes []byte) (Lock, error) {
		ret, err := AddressED25519FromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		example := AddressED25519Null()
		addrBack, err := AddressED25519FromBytes(example.Bytes())
		util.AssertNoError(err)
		util.Assertf(EqualConstraints(addrBack, AddressED25519Null()), "inconsistency "+AddressED25519Name)

		_, err = lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)
	})
}
