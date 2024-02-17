package glb

import (
	"sync"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/spf13/viper"
)

var displayEndpointOnce sync.Once

func GetClient() *client.APIClient {
	endpoint := viper.GetString("api.endpoint")
	Assertf(endpoint != "", "node API endpoint not specified")
	displayEndpointOnce.Do(func() {
		Infof("using API endpoint: %s", endpoint)
	})
	return client.New(endpoint)
}

func InitLedgerFromNode() {
	ledgerID, err := GetClient().GetLedgerID()
	AssertNoError(err)
	ledger.Init(ledgerID)
	Infof("successfully connected to the node at %s", viper.GetString("api.endpoint"))
}
