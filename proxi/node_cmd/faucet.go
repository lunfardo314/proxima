package node_cmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/commands"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const getFundsPath = "/"

type faucetConfig struct {
	outputAmount uint64
	port         uint64
	addr         string
}

type faucet struct {
	cfg        faucetConfig
	walletData glb.WalletData
}

const (
	defaultFaucetOutputAmount = 1_000_000
	defaultFaucetPort         = 9500
)

func initFaucetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "faucet",
		Short: `starts a faucet server`,
		Args:  cobra.NoArgs,
		Run:   runFaucetCmd,
	}

	cmd.PersistentFlags().Uint64("faucet.output_amount", defaultFaucetOutputAmount, "amount to send to the requester")
	err := viper.BindPFlag("faucet.output_amount", cmd.PersistentFlags().Lookup("faucet.output_amount"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().Uint64("faucet.port", defaultFaucetPort, "faucet port")
	err = viper.BindPFlag("faucet.port", cmd.PersistentFlags().Lookup("faucet.port"))
	glb.AssertNoError(err)

	return cmd
}

func readFaucetConfigIn(sub *viper.Viper) (ret faucetConfig) {
	//glb.Assertf(sub != nil, "faucet configuration is not available")
	if sub != nil {
		ret.outputAmount = sub.GetUint64("output_amount")
		ret.port = sub.GetUint64("port")
		ret.addr = sub.GetString("addr")
	} else {
		// get default values
		ret.outputAmount = viper.GetUint64("faucet.output_amount")
		ret.port = viper.GetUint64("faucet.port")
		ret.addr = viper.GetString("faucet.addr")
	}
	return
}

func displayFaucetConfig() faucetConfig {
	cfg := readFaucetConfigIn(viper.Sub("faucet"))
	glb.Infof("faucet configuration:")
	glb.Infof("     output amount: %d", cfg.outputAmount)
	glb.Infof("     port:          %d", cfg.port)

	return cfg
}

func runFaucetCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()
	glb.Assertf(walletData.Sequencer != nil, "can't get own sequencer ID")
	glb.Infof("sequencer ID (funds source): %s", walletData.Sequencer.String())
	cfg := displayFaucetConfig()
	fct := &faucet{
		cfg:        cfg,
		walletData: walletData,
	}
	fct.faucetServer()
}

const ownSequencerCmdFee = 50

func (fct *faucet) handler(w http.ResponseWriter, r *http.Request) {
	targetStr, ok := r.URL.Query()["addr"]
	if !ok || len(targetStr) != 1 {
		writeResponse(w, "wrong parameter 'addr' in request 'get_funds'")
		return
	}

	glb.Infof("Sending funds to %s", targetStr[0])
	targetLock, err := ledger.AccountableFromSource(targetStr[0])
	if err != nil {
		glb.Infof("Error from AccountableFromSource: %s", err.Error())
		writeResponse(w, err.Error())
		return
	}
	glb.Infof("querying wallet's outputs..")
	walletOutputs, lrbid, err := glb.GetClient().GetAccountOutputs(fct.walletData.Account, func(_ *ledger.OutputID, o *ledger.Output) bool {
		return o.NumConstraints() == 2
	})
	if err != nil {
		glb.Infof("Error from GetAccountOutputs: %s", err.Error())
		writeResponse(w, err.Error())
		return
	}

	glb.PrintLRB(lrbid)
	glb.Infof("will be using %d tokens as tag-along fee. Outputs in the wallet:", ownSequencerCmdFee)
	for i, o := range walletOutputs {
		glb.Infof("%d : %s : %s", i, o.ID.StringShort(), util.Th(o.Output.Amount()))
	}

	cmdConstr, err := commands.MakeSequencerWithdrawCommand(fct.cfg.outputAmount, targetLock.AsLock())
	if err != nil {
		glb.Infof("Error from MakeSequencerWithdrawCommand: %s", err.Error())
		writeResponse(w, err.Error())
		return
	}

	transferData := txbuilder.NewTransferData(fct.walletData.PrivateKey, fct.walletData.Account, ledger.TimeNow()).
		WithAmount(ownSequencerCmdFee).
		WithTargetLock(ledger.ChainLockFromChainID(*fct.walletData.Sequencer)).
		MustWithInputs(walletOutputs...).
		WithSender().
		WithConstraint(cmdConstr)

	txBytes, err := txbuilder.MakeSimpleTransferTransaction(transferData)
	if err != nil {
		glb.Infof("Error from MakeSimpleTransferTransaction: %s", err.Error())
		writeResponse(w, err.Error())
		return
	}

	txid, err := transaction.IDFromTransactionBytes(txBytes)
	glb.AssertNoError(err)

	err = glb.GetClient().SubmitTransaction(txBytes)
	if err != nil {
		glb.Infof("Error from SubmitTransaction: %s", err.Error())
		writeResponse(w, err.Error())
		return
	}

	glb.Infof("requested faucet transfer of %s tokens to %s from sequencer %s...",
		util.Th(fct.cfg.outputAmount), targetLock.String(), fct.walletData.Sequencer.StringShort())
	glb.Infof("             transaction %s (hex = %s)", txid.String(), txid.StringHex())

	writeResponse(w, "") // send ok
}

func writeResponse(w http.ResponseWriter, respStr string) {
	var respBytes []byte
	var err error
	if len(respStr) > 0 {
		respBytes, err = json.Marshal(&api.Error{Error: respStr})
	} else {
		respBytes, err = json.Marshal(&api.Error{})
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = w.Write(respBytes)
	util.AssertNoError(err)
}

func (fct *faucet) faucetServer() {
	http.HandleFunc(getFundsPath, fct.handler) // Route for the handler function
	sport := fmt.Sprintf(":%d", fct.cfg.port)
	glb.Infof("running proxi faucet server on %s. Press Ctrl-C to stop..", sport)
	glb.AssertNoError(http.ListenAndServe(sport, nil))
}

func initGetFundsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "getfunds",
		Short: `requests funds from a faucet`,
		Args:  cobra.NoArgs,
		Run:   getFundsCmd,
	}

	cmd.PersistentFlags().Uint64("faucet.port", 9500, "faucet port")
	err := viper.BindPFlag("faucet.port", cmd.PersistentFlags().Lookup("faucet.port"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().String("faucet.addr", "http://113.30.191.219", "faucet address")
	err = viper.BindPFlag("faucet.addr", cmd.PersistentFlags().Lookup("faucet.addr"))
	glb.AssertNoError(err)

	return cmd
}

func getFundsCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()
	cfg := readFaucetConfigIn(viper.Sub("faucet"))

	faucetAddr := fmt.Sprintf("%s:%d", cfg.addr, cfg.port)

	glb.Infof("requesting funds from: %s", faucetAddr)

	path := fmt.Sprintf(getFundsPath+"?addr=%s", walletData.Account.String())

	c := client.NewWithGoogleDNS(faucetAddr)
	_, err := c.Get(path)
	if err != nil {
		glb.Infof("error requesting funds from: %s", err.Error())
	} else {
		glb.Infof("Funds requested successfully!")
	}
}
