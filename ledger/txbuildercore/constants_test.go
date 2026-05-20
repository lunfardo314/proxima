package txbuildercore_test

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// TestConstants_JSONRoundTrip verifies that a Constants value
// marshals and unmarshals losslessly and that the wire form uses
// JSON integers for numeric fields and plain hex (no 0x prefix)
// for Hash + GenesisControllerPublicKey.
func TestConstants_JSONRoundTrip(t *testing.T) {
	var hashBytes [32]byte
	for i := range hashBytes {
		hashBytes[i] = byte(i + 1)
	}
	pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
	for i := range pub {
		pub[i] = byte(i + 100)
	}

	orig := &txbuildercore.Constants{
		Hash:                         hashBytes,
		Description:                  "test ledger",
		GenesisControllerPublicKey:   pub,
		GenesisTimeUnix:              1_700_000_000,
		TickDuration:                 100 * time.Millisecond,
		TicksPerSlot:                 128,
		InitialSupply:                1_000_000_000_000_000,
		SlotInflationBase:            1_000_000,
		MinimumInflatableAmount0:     1_000_000_000,
		TransactionPace:              12,
		TransactionPaceSequencer:     3,
		MaxNumberOfEndorsements:      8,
		PreBranchConsolidationTicks:  0,
		SafeRevocationSlots:          1024,
		DelegationEpochSlots:         600,
		MaxFrozenEpochs:              32,
		DelegationEpochSlotsMin:      100,
		DelegationEpochSlotsMax:      10_000,
		DelegationMaxFrozenEpochsMin: 1,
		DelegationMaxFrozenEpochsMax: 64,
		TagAlongSlots:                300,
		TagAlongReclaimSlots:         900,
		AttachmentCostBudget:         550,
		TxIDStateTTLSlots:            7200,
		HealthyCoverageNumerator:     1,
		HealthyCoverageDenominator:   2,
	}

	data, err := json.Marshal(orig)
	require.NoError(t, err)

	s := string(data)
	// Numeric fields are JSON integers (not quoted).
	require.Contains(t, s, `"attachment_cost_budget":550`)
	require.Contains(t, s, `"transaction_pace":12`)
	require.Contains(t, s, `"tick_duration_ns":100000000`)
	// Hash and pubkey are plain hex strings (no 0x prefix).
	require.Contains(t, s, `"hash":"`+hex.EncodeToString(hashBytes[:])+`"`)
	require.Contains(t, s, `"genesis_controller_public_key":"`+hex.EncodeToString(pub)+`"`)
	// Defensive: no "0x" prefix anywhere on hex values.
	require.False(t, strings.Contains(s, `"0x`), "wire form must not carry 0x prefixes: %s", s)

	var got txbuildercore.Constants
	require.NoError(t, json.Unmarshal(data, &got))
	require.Equal(t, *orig, got)
}

// TestConstants_PureFunctions sanity-checks the clock helpers don't
// depend on the ledger singleton.
func TestConstants_PureFunctions(t *testing.T) {
	c := &txbuildercore.Constants{
		GenesisTimeUnix: 1_700_000_000,
		TickDuration:    100 * time.Millisecond,
		TicksPerSlot:    128,
	}
	gen := c.GenesisTime()
	require.Equal(t, int64(1_700_000_000), gen.Unix())

	// 1 tick after genesis -> ticksSinceGenesis == 1.
	require.Equal(t, int64(1), c.TimeToTicksSinceGenesis(gen.Add(c.TickDuration)))

	// One slot worth of wall-clock time.
	require.Equal(t, time.Duration(128)*100*time.Millisecond, c.SlotDuration())
}
