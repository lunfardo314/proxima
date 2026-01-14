package ledger

// This file defines the minimal ledger identity stored at the trie root.
// The identity contains only truly immutable data that identifies the ledger:
// - Genesis time (unix timestamp)
// - Description string
//
// All other constants (including library functions) are stored in the
// upgrade DB partition and can evolve through upgrades.

import (
	"encoding/binary"
	"fmt"
)

// LedgerIdentity contains the minimal immutable data at the trie root.
// This identifies a specific ledger instance.
type LedgerIdentity struct {
	GenesisTimeUnix uint32
	Description     string
}

// LedgerIdentityVersion is the format version for future compatibility.
// Version 1 is the initial format after the upgrade system refactor.
const LedgerIdentityVersion = byte(1)

// MaxDescriptionLength limits the description to fit in a single byte length prefix.
const MaxDescriptionLength = 255

// NewLedgerIdentity creates a new ledger identity from parameters.
func NewLedgerIdentity(genesisTimeUnix uint32, description string) *LedgerIdentity {
	if len(description) > MaxDescriptionLength {
		description = description[:MaxDescriptionLength]
	}
	return &LedgerIdentity{
		GenesisTimeUnix: genesisTimeUnix,
		Description:     description,
	}
}

// LedgerIdentityFromInitParams creates ledger identity from init parameters.
func LedgerIdentityFromInitParams(params InitParameters) *LedgerIdentity {
	return NewLedgerIdentity(params.GenesisTimeUnix, params.Description)
}

// Bytes serializes the ledger identity to bytes.
// Format:
//   - 1 byte: version
//   - 4 bytes: genesis time unix (big-endian)
//   - 1 byte: description length
//   - N bytes: description
func (id *LedgerIdentity) Bytes() []byte {
	descLen := len(id.Description)
	if descLen > MaxDescriptionLength {
		descLen = MaxDescriptionLength
	}

	buf := make([]byte, 1+4+1+descLen)
	buf[0] = LedgerIdentityVersion
	binary.BigEndian.PutUint32(buf[1:5], id.GenesisTimeUnix)
	buf[5] = byte(descLen)
	copy(buf[6:], id.Description[:descLen])
	return buf
}

// LedgerIdentityFromBytes deserializes ledger identity from bytes.
func LedgerIdentityFromBytes(data []byte) (*LedgerIdentity, error) {
	if len(data) < 6 {
		return nil, fmt.Errorf("ledger identity data too short: %d bytes", len(data))
	}

	version := data[0]
	if version != LedgerIdentityVersion {
		return nil, fmt.Errorf("unsupported ledger identity version: %d (expected %d)", version, LedgerIdentityVersion)
	}

	genesisTime := binary.BigEndian.Uint32(data[1:5])
	descLen := int(data[5])

	if len(data) < 6+descLen {
		return nil, fmt.Errorf("ledger identity data truncated: expected %d bytes, got %d", 6+descLen, len(data))
	}

	return &LedgerIdentity{
		GenesisTimeUnix: genesisTime,
		Description:     string(data[6 : 6+descLen]),
	}, nil
}

// String returns a human-readable representation.
func (id *LedgerIdentity) String() string {
	return fmt.Sprintf("LedgerIdentity{genesis=%d, description=%q}", id.GenesisTimeUnix, id.Description)
}
