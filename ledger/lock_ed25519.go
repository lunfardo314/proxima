package ledger

import (
	"crypto/ed25519"
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
	//AddressED25519Name     = "addressED25519"
	AddressED25519Name     = "a"
	addressED25519Template = AddressED25519Name + "(0x%s)"
)

// AddressED25519FromBytesAtSlot parses an AddressED25519 using the library for the given slot.
func AddressED25519FromBytesAtSlot(data []byte, slot uint32) (AddressED25519, error) {
	lib := L(slot)
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 1)
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

// AddressED25519FromBytes parses an AddressED25519 using the latest library version.
// Deprecated: Use AddressED25519FromBytesAtSlot for parsing historical bytecode.
func AddressED25519FromBytes(data []byte) (AddressED25519, error) {
	return AddressED25519FromBytesAtSlot(data, base.MaxSlot)
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

func registerAddressED25519Constraint(lib *Library) {
	lib.mustRegisterConstraint(AddressED25519Name, 1, func(data []byte) (Constraint, error) {
		return AddressED25519FromBytes(data)
	}, initTestAddressED25519Constraint)
	lib.mustRegisterLock(AddressED25519Name, func(bytes []byte) (Lock, error) {
		ret, err := AddressED25519FromBytes(bytes)
		if err != nil {
			return nil, err
		}
		return ret, nil
	})
}

func initTestAddressED25519Constraint() {
	example := AddressED25519Null()
	addrBack, err := AddressED25519FromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(addrBack, AddressED25519Null()), "inconsistency "+AddressED25519Name)

	_, err = L(base.MaxSlot).ParsePrefixBytecode(example.Bytes())
	util.AssertNoError(err)
}

const addressED25519ConstraintSource = `

// DEPRECATED: signature verification is correct but not necessary, because validity of the signature is 
// checked for the transaction anyway.
// We are leaving it just as an example 
// $0 = address data 32 bytes
// $1 = signature
// $2 = public key
// return true if transaction essence signature is valid for the address
//func unlockedWithSigED25519: and(
//	equal($0, blake2b($2)), 		       // address in the address data must be equal to the hash of the public key
//	validSignatureED25519(txID, $1, $2)
//)

// ED25519 address constraint wraps 32 bytes address, the blake2b hash of the public key
// For example expression 'addressED25519(0x010203040506..)' used as constraint in the output makes 
// the output unlockable only with the presence of signature corresponding 
// to the address '0x010203040506..'

// 'unlockedByReference'' specifies validation of the input unlock with the reference.
// The referenced constraint must be exactly the same  but with strictly lesser index.
// This prevents from cycles and forces some other unlock mechanism up in the list of outputs
// $0 self unlock parameters
func unlockedByReference: and(
    equal(len($0), u64/1),                     // prevent panic in compound locks
	lessThan($0, selfOutputIndex),             // unlock parameter must point to another input with 
                                               // strictly smaller index. This prevents reference cycles	
	equal(self, consumedConstraintByIndex($0, lockConstraintIndex))  // the referenced constraint bytes must be equal to the self constraint bytes
)

// $0 selfUnlockParameters
func _referencedIndex : if( isZero(len($0)), 0xff, byte($0,0))

// if it is 'produced' invocation context (constraint invoked in the input), only size of the address is checked
// Otherwise the first will check first condition if it is unlocked by reference, otherwise checks unlocking signature
// $0 - ED25519 address, 32 byte blake2b hash of the public key
// Unlock data is 1 byte with reference index to the previous input or signature unlock with 0xff
func addressED25519: and(
	require(equal(selfBlockIndex,1), !!!locks_must_be_at_block_1), 
	or(
		and(
			selfIsProducedOutput, 
			equal(len($0), u64/32) 
		),
		and(
			selfIsConsumedOutput, 
			or(
					// if it is unlocked with reference, the signature is not checked
				unlockedByReference(_referencedIndex(selfUnlockParameters)),
					// checks if tx signature corresponds to the address
                equal($0, blake2b(publicKeyED25519(txSignature)))
				// deprecated: unlockedWithSigED25519($0, signatureED25519(txSignature), publicKeyED25519(txSignature)) 
			)
		)
	)
)

// short form of lock a(<hex bytes>)
// $0 - ED25519 address, 32 byte blake2b hash of the public key
func a : addressED25519($0)

`
