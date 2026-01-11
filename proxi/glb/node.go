package glb

import (
	"encoding/hex"
	"sync"
	"time"

	"github.com/lunfardo314/easyfl"
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

func InitLedgerFromNode() {
	ledgerIDData, err := GetClient().GetLedgerIdentityData()
	AssertNoError(err)
	// pre-parse
	fromYAML, err := easyfl.ReadLibraryFromYAML(ledgerIDData)
	if err != nil {
		Infof("failed to parse ledger definition")
		if IsVerbose() {
			Infof("easyfl.ReadLibraryFromYAML returned '%v'", err)
		}
		Assertf(false, "exit")
		return
	}
	Infof("successfully parsed ledger definitions. Library hash = %s", fromYAML.Hash)

	ledger.MustInitSingleton(ledgerIDData)
	Infof("successfully connected to the node at %s", viper.GetString("api.endpoint"))
	Infof("verbose = %v", IsVerbose())
	h := ledger.L(base.MaxSlot).LibraryHash()
	Infof("ledger library hash: %s", hex.EncodeToString(h[:]))
}
