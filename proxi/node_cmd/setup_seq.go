package node_cmd

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/keystore"
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
	// Sequencers default to attaching delegationParams (default-on);
	// pass --no-delegations to create a sequencer chain that cannot
	// accept delegations.
	addDelegationParamsFlags(seqSendCmd, true)
	seqSendCmd.InitDefaultHelpCmd()
	return seqSendCmd
}

func runSeqSetupCmd(_ *cobra.Command, args []string) {
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

		// wait for available funds
		waitForFunds(accountable, amount)

		// proxi node mkchain 1000000000000
		_, cid, txid, err := MakeChain(amount)
		glb.AssertNoError(err)
		glb.Infof("new chain id is %s", cid.String())
		if !glb.NoWait() {
			glb.TrackTxInclusion(txid, time.Second)
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

// getChainIdForAccount scans all chain outputs and returns the
// ChainID of any non-delegation chain whose controller equals the
// given account. Pure wallet-side lock-symbol + index-values parse;
// no ledger.L() singleton.
func getChainIdForAccount(account ledger.Controller) *base.ChainID {
	lib := glb.GetTxLibrary()
	clnt := glb.GetClient()
	chains, _, err := clnt.GetAllChains()
	glb.AssertNoError(err)
	accountHID := account.ControllerID()
	for _, o := range chains {
		lockBin, err := o.Output.ConstraintAt(ledger.ConstraintIndexLock)
		if err != nil {
			continue
		}
		sym, _, _, err := lib.ParseBytecodeOneLevel(lockBin)
		if err != nil || sym == txbuildercore.DelegateLockName {
			// only non-delegation chain outputs (sigLock / chainLock /
			// foundry) qualify as candidates for "this is my chain".
			continue
		}
		// For sigLock / chainLock the controller bytes live at
		// index-values[0]. Comparing raw bytes avoids reaching for
		// any typed Lock dispatch.
		ivBin, err := o.Output.ConstraintAt(ledger.ConstraintIndexIndexValues)
		if err != nil {
			continue
		}
		vals, err := txbuildercore.DecodeIndexValuesTuple(ivBin)
		if err != nil || len(vals) == 0 {
			continue
		}
		if bytes.Equal(vals[0], accountHID) {
			return &o.ChainID
		}
	}
	return nil
}

func waitForFunds(accountable ledger.Controller, amount uint64) {
	for {
		res, err := glb.GetClient().GetOutputsForControllerID(accountable.ControllerID(), client.GetOutputsParams{
			LockType:  api.GetOutputsLockTypeSigLock,
			Chained:   client.NonChainedOnly(),
			ForAmount: amount,
		})
		glb.AssertNoError(err)
		if res.AvailableAmount >= amount {
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
	err = os.WriteFile("proxi.yaml", modifiedData, 0600)
	glb.AssertNoError(err)
}

func updateNodeConfig(name string, key ed25519.PrivateKey, chainId base.ChainID) {
	// Create a JSON keystore file for the sequencer controller key
	publicKey := key.Public().(ed25519.PublicKey)
	sid := base.HolderIDFromPublicKey(base.SignatureTypeED25519, publicKey)
	holderID := hex.EncodeToString(sid[:])
	seqKeyFile := keystore.DefaultKeyFile

	ks, err := keystore.NewUnencrypted(keystore.KeyTypeED25519, key, publicKey, holderID)
	glb.AssertNoError(err)

	// Offer encryption
	if glb.YesNoPrompt("Encrypt the sequencer key file with a passphrase?", false) {
		passphrase := glb.ReadPassphraseConfirm()
		ks, err = keystore.EncryptKeystore(ks, passphrase, "")
		glb.AssertNoError(err)
		glb.Infof("Key encrypted.")
	}

	err = ks.SaveToFile(seqKeyFile)
	glb.AssertNoError(err)
	glb.Infof("sequencer controller key saved to '%s'", seqKeyFile)

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
		sequencer["enable"] = true
		sequencer["chain_id"] = chainId.StringHex()
		sequencer["controller_key_file"] = seqKeyFile
		// Remove inline key if previously set (from old configs)
		delete(sequencer, "controller_key")
	} else {
		glb.Infof("!!! Error sequencer key not found")
	}

	// Marshal the modified config back to YAML
	modifiedData, err := yaml.Marshal(&config)
	glb.AssertNoError(err)

	// Write the modified YAML back to the file
	err = os.WriteFile("proxima.yaml", modifiedData, 0600)
	glb.AssertNoError(err)
}
