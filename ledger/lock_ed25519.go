package ledger

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

type AddressED25519 []byte

const (
	AddressED25519Name     = "addressED25519"
	addressED25519Template = AddressED25519Name + "(0x%s)"
)

func AddressED25519FromBytes(data []byte) (AddressED25519, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, err
	}
	if sym != AddressED25519Name {
		return nil, fmt.Errorf("not an AddressED25519")
	}
	addrBin := easyfl.StripDataPrefix(args[0])
	if len(addrBin) != 32 {
		return nil, fmt.Errorf("wrong data length")
	}
	return addrBin, nil
}

func AddressED25519FromSource(src string) (AddressED25519, error) {
	bin, err := binFromSource(src)
	if err != nil {
		return nil, fmt.Errorf("EasyFL compile error: %v", err)
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

func (a AddressED25519) source() string {
	return fmt.Sprintf(addressED25519Template, hex.EncodeToString(a))
}

func (a AddressED25519) Bytes() []byte {
	return mustBinFromSource(a.source())
}

func (a AddressED25519) Clone() AddressED25519 {
	return slices.Clone(a)
}

func (a AddressED25519) Accounts() []Accountable {
	return []Accountable{a}
}

func (a AddressED25519) UnlockableWith(acc AccountID, _ ...Time) bool {
	return bytes.Equal(a.AccountID(), acc)
}

func (a AddressED25519) AccountID() AccountID {
	return a.Bytes()
}

func (a AddressED25519) Name() string {
	return AddressED25519Name
}

func (a AddressED25519) String() string {
	return a.source()
}

func (a AddressED25519) Short() string {
	return fmt.Sprintf(addressED25519Template, hex.EncodeToString(a)[:8]+"..")
}

func (a AddressED25519) AsLock() Lock {
	return a
}

func initAddressED25519Constraint() {
	L().MustExtendMany(AddressED25519ConstraintSource)

	example := AddressED25519Null()
	addrBack, err := AddressED25519FromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(addrBack, AddressED25519Null()), "inconsistency "+AddressED25519Name)

	prefix, err := L().ParseBytecodePrefix(example.Bytes())
	util.AssertNoError(err)

	registerConstraint(AddressED25519Name, prefix, func(data []byte) (Constraint, error) {
		return AddressED25519FromBytes(data)
	})
}

const AddressED25519ConstraintSource = `

// ED25519 address constraint wraps 32 bytes address, the blake2b hash of the public key
// For example expression 'addressED25519(0x010203040506..)' used as constraint in the output makes 
// the output unlockable only with the presence of signature corresponding 
// to the address '0x010203040506..'

// $0 = address data 32 bytes
// $1 = signature
// $2 = public key
// return true if transaction essence signature is valid for the address
func unlockedWithSigED25519: and(
	equal($0, blake2b($2)), 		       // address in the address data must be equal to the hash of the public key
	validSignatureED25519(txEssenceBytes, $1, $2)
)

// 'unlockedByReference'' specifies validation of the input unlock with the reference.
// The referenced constraint must be exactly the same  but with strictly lesser index.
// This prevents from cycles and forces some other unlock mechanism up in the list of outputs
func unlockedByReference: and(
	lessThan(selfUnlockParameters, selfOutputIndex),              // unlock parameter must point to another input with 
							                                      // strictly smaller index. This prevents reference cycles	
	equal(self, consumedLockByInputIndex(selfUnlockParameters))  // the referenced constraint bytes must be equal to the self constraint bytes
)

// if it is 'produced' invocation context (constraint invoked in the input), only size of the address is checked
// Otherwise the first will check first condition if it is unlocked by reference, otherwise checks unlocking signature
// Second condition not evaluated if the first is true
// $0 - ED25519 address, 32 byte blake2b hash of the public key
func addressED25519: and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
	selfMustStandardAmount,
	or(
		and(
			selfIsProducedOutput, 
			equal(len8($0), 32) 
		),
		and(
			selfIsConsumedOutput, 
			or(
					// if it is unlocked with reference, the signature is not checked
				unlockedByReference,
					// tx signature is checked
				unlockedWithSigED25519($0, signatureED25519(txSignature), publicKeyED25519(txSignature)) 
			)
		),
		!!!addressED25519_unlock_failed
	)
)
`
