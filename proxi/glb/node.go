package glb

import (
	"encoding/hex"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/spf13/viper"
)

var (
	UseAlternativeTagAlongSequencer bool
	TargetInclusionDepth            int
)

var displayEndpointOnce sync.Once

func GetClient(endpoint ...string) *client.APIClient {
	endp := ""
	if len(endpoint) > 0 {
		endp = endpoint[0]
	} else {
		endp = viper.GetString("api.endpoint")
	}
	Assertf(endp != "", "GetClient: node API endpoint not specified")
	var timeout []time.Duration
	if timeoutSec := viper.GetInt("api.timeout_sec"); timeoutSec > 0 {
		timeout = []time.Duration{time.Duration(timeoutSec) * time.Second}
	}
	displayEndpointOnce.Do(func() {
		if len(timeout) == 0 {
			Infof("API endpoint: %s, default timeout", endp)
		} else {
			Infof("API endpoint: %s, timeout: %v", endp, timeout[0])
		}
	})
	return client.NewWithGoogleDNS(endp, timeout...)
}

// InitLedgerFromNode populates the global ledger.L() singleton with
// the library JSON(s) fetched from the node. After the wasm-style
// refactor most proxi commands no longer call this; they go through
// glb.GetTxLibrary() + glb.GetLedgerConstants() instead.
//
// Surviving callers (singleton-dependent on purpose):
//   - proxi/node_cmd/chess_cmd/* — kept as the in-tree typed-builder
//     + singleton reference; chess_poc itself uses ledger.L() internally.
//   - proxi/util_cmd/inflation.go — eval-bound ChainInflationMultiStep.
//   - proxi/snapshot_cmd/check.go — typed multistate snapshot parsers.
//
// proxi/glb/wallet_recipes.go and proxi/node_cmd/faucet_{srv,get}.go
// also call into it but are themselves commented off; they'll come
// back together when the faucet is ported to txbuildercore.
func InitLedgerFromNode() {
	clnt := GetClient()

	// Fetch all upgrade libraries by walking the upgrade chain from latest back to genesis
	libraries := make(map[uint32][]byte)
	resp, err := clnt.GetLedgerDefinition(nil)
	AssertNoError(err)

	libraries[resp.UpgradeSlot] = []byte(resp.LibraryJSON)
	Infof("fetched library for slot %d, hash = %s", resp.UpgradeSlot, resp.LibraryHash)

	// Walk back through previous upgrades until we reach genesis (slot 0)
	for resp.UpgradeSlot > 0 {
		prevSlot := resp.PrevUpgradeSlot
		resp, err = clnt.GetLedgerDefinition(&prevSlot)
		AssertNoError(err)
		libraries[resp.UpgradeSlot] = []byte(resp.LibraryJSON)
		Infof("fetched library for slot %d, hash = %s", resp.UpgradeSlot, resp.LibraryHash)
	}

	ledger.MustInitLibraryCacheFromMap(libraries)
	Infof("successfully connected to the node at %s", viper.GetString("api.endpoint"))
	Infof("verbose = %v", IsVerbose())
	h := ledger.L(base.MaxSlot).LibraryHash()
	Infof("ledger library hash: %s", hex.EncodeToString(h[:]))
}
