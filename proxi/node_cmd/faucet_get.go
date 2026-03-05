package node_cmd

import (
	"fmt"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const defaultFaucetHostIPAddr = "113.30.191.219"

func initGetFundsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "getfunds",
		Short: `request funds from the faucet`,
		Args:  cobra.NoArgs,
		Run:   getFundsCmd,
	}

	cmd.PersistentFlags().Uint("faucet.port", defaultFaucetPort, "faucet port")
	err := viper.BindPFlag("faucet.port", cmd.PersistentFlags().Lookup("faucet.port"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().String("faucet.host", defaultFaucetHostIPAddr, "faucet host address")
	err = viper.BindPFlag("faucet.host", cmd.PersistentFlags().Lookup("faucet.host"))
	glb.AssertNoError(err)

	return cmd
}

func getFundsCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()
	walletAccount := glb.GetWalletAccount()
	faucetURL := fmt.Sprintf("http://%s:%d", viper.GetString("faucet.host"), viper.GetUint("faucet.port"))

	glb.Infof("requesting funds from faucet at %s", faucetURL)

	path := fmt.Sprintf(getFundsPath+"?addr=%s", walletAccount.String())

	c := client.NewWithGoogleDNS(faucetURL)
	answer, err := c.Get(path)

	if err != nil || len(answer) > 2 {
		if err != nil {
			glb.Infof("error requesting funds from: %s", err.Error())
		} else {
			glb.Infof("error requesting funds from: %s", string(answer))
		}
	} else {
		glb.Infof("Funds requested successfully!")
	}
}
