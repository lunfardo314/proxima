package node_cmd

import (
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
)

func initSeqSetupCmd() *cobra.Command {
	seqSendCmd := &cobra.Command{
		Use:     "setup_seq <name> [<amount>]",
		Aliases: util.List("send"),
		Short:   `setup a sequencer with name and amount`,
		Args:    cobra.RangeArgs(1, 2),
		Run:     runSeqSetupCmd,
	}
	seqSendCmd.InitDefaultHelpCmd()
	return seqSendCmd
}

func runSeqSetupCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()

	glb.Infof("wallet account is: %s", walletData.Account.String())
	accountable := glb.MustGetTarget()

	name := args[0]

	glb.Infof("name: %s", name)

	var chainId *base.ChainID = nil
	if len(args) > 1 {
		// create a chain
		amount, err := strconv.ParseUint(args[1], 10, 64)
		glb.AssertNoError(err)

		glb.Infof("amount: %s", util.Th(amount))
		if amount < ledger.L().ID.MinimumAmountOnSequencer {
			glb.Infof("minimum amout required: %d", ledger.L().ID.MinimumAmountOnSequencer)
			return
		}

		// wait for available funds
		waitForFunds(accountable, amount)

		// proxi node mkchain 1000000000000
		txCtx, cid, err := MakeChain(amount)
		glb.AssertNoError(err)
		glb.Infof("new chain id is %s", cid.String())
		if !glb.NoWait() {
			glb.TrackTxInclusion(txCtx.ID(), time.Second)
		}
		chainId = &cid
	} else {
		// search for chain
		chainId = getChainIdForAccount(walletData.Account)
		if chainId == nil {
			glb.Fatalf("chain id not found for account %s", walletData.Account.String())
		} else {
			glb.Infof("found chain id: %s", chainId.StringHex())
		}
	}
	if chainId != nil {
		// update proxi.yaml with chain id
		updateWalletConfig(*chainId)

		// update proxima.yaml
		updateNodeConfig(name, walletData.PrivateKey, *chainId)
	}
}

func getChainIdForAccount(account ledger.Accountable) *base.ChainID {
	clnt := glb.GetClient()
	chains, _, err := clnt.GetAllChains()
	glb.AssertNoError(err)
	for _, o := range chains {
		lock := o.Output.Lock()

		if o.Output.Lock().Name() == ledger.DelegateLockName {
			// only sequencer chain
			continue
		}

		if account.String() == lock.String() {
			return &o.ChainID
		}
	}

	return nil
}

func waitForFunds(accountable ledger.Accountable, amount uint64) {
	for {
		sumOutsideChains := uint64(0)
		outs, _, err := glb.GetClient().GetAccountOutputs(accountable)
		glb.AssertNoError(err)
		for _, o := range outs {
			if _, idx := o.Output.ChainConstraint(); idx != 0xff {
			} else {
				sumOutsideChains += o.Output.TokenBalance()
			}
		}
		if sumOutsideChains >= amount {
			break
		}
		time.Sleep(1 * time.Second)
	}
}

func updateWalletConfig(chainId base.ChainID) {
	// Read the YAML file
	data, err := os.ReadFile("proxi.yaml")
	glb.AssertNoError(err)

	// Unmarshal the YAML file into a generic map
	var config map[string]interface{}
	err = yaml.Unmarshal(data, &config)
	glb.AssertNoError(err)

	// Navigate to the specific field and modify it
	if wallet, ok := config["wallet"].(map[interface{}]interface{}); ok {
		wallet["sequencer_id"] = chainId.StringHex()
	}

	// Marshal the modified config back to YAML
	modifiedData, err := yaml.Marshal(&config)
	glb.AssertNoError(err)

	// Write the modified YAML back to the file
	err = os.WriteFile("proxi.yaml", modifiedData, 0666)
	glb.AssertNoError(err)
}

func updateNodeConfig(name string, key ed25519.PrivateKey, chainId base.ChainID) {
	// Read the YAML file
	data, err := os.ReadFile("proxima.yaml")
	glb.AssertNoError(err)

	// Unmarshal the YAML file into a generic map
	var config map[string]interface{}
	err = yaml.Unmarshal(data, &config)
	glb.AssertNoError(err)

	// Access the "sequencer" section and update its fields
	if sequencer, ok := config["sequencer"].(map[interface{}]interface{}); ok {
		sequencer["name"] = name
		sequencer["enable"] = true // Enable the sequencer
		sequencer["chain_id"] = chainId.StringHex()
		sequencer["controller_key"] = hex.EncodeToString(key)
	} else {
		glb.Infof("!!! Error sequencer key not found")
	}

	// Marshal the modified config back to YAML
	modifiedData, err := yaml.Marshal(&config)
	glb.AssertNoError(err)

	// Write the modified YAML back to the file
	err = os.WriteFile("proxima.yaml", modifiedData, 0666)
	glb.AssertNoError(err)
}
