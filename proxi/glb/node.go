package glb

import (
	"sync"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/spf13/viper"
)

var displayEndpointOnce sync.Once

func GetClient() *client.APIClient {
	endpoint := viper.GetString("api.endpoint")
	Assertf(endpoint != "", "node API endpoint not specified")
	var timeout []time.Duration
	if timeoutSec := viper.GetInt("api.timeout_sec"); timeoutSec > 0 {
		timeout = []time.Duration{time.Duration(timeoutSec) * time.Second}
	}
	displayEndpointOnce.Do(func() {
		if len(timeout) == 0 {
			Infof("using API endpoint: %s, default timeout", endpoint)
		} else {
			Infof("using API endpoint: %s, timeout: %v", endpoint, timeout[0])
		}
	})
	return client.New(endpoint, timeout...)
}

func InitLedgerFromNode() {
	ledgerID, err := GetClient().GetLedgerID()
	AssertNoError(err)
	ledger.Init(ledgerID)
	Infof("successfully connected to the node at %s", viper.GetString("api.endpoint"))
}
