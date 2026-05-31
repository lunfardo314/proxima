package ledger

import (
	_ "embed"
)

// Output-kind index. Spec: claude/output_kind_index.md.
//
// The EasyFL side (def/output_kind.easyfl) defines the kind-tag constants plus
// the _enforceChainFamily / _enforceKindTag helpers. This file mirrors the tag
// values for the Go builders, which write the tag as the last member of the
// index-values tuple.

//go:embed def/output_kind.easyfl
var outputKindSource string

// Kind tags — 4-byte values, mirrored in def/output_kind.easyfl. Chain tags
// share the '$' (0x24) family byte; STEM is standalone. DEX keeps its own
// "ORDR" tag (see dexOrderBookPrefix).
var (
	GenericChainKindTag = []byte{'$', 'G', 'E', 'N'}
	FoundryKindTag      = []byte{'$', 'F', 'N', 'D'}
	SequencerKindTag    = []byte{'$', 'S', 'E', 'Q'}
	DelegationKindTag   = []byte{'$', 'D', 'L', 'G'}
	StemKindTag         = []byte{'S', 'T', 'E', 'M'}
)

// ChainKindFamilyByte is the first byte shared by every chain kind tag; a prefix
// scan on it enumerates all chains regardless of role.
const ChainKindFamilyByte = byte('$')
