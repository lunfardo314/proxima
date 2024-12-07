package node_cmd

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

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
	redrawFromChain    bool
	outputAmount       uint64
	port               uint64
	addr               string
	account            ledger.Accountable
	privKey            ed25519.PrivateKey
	maxRequestsPerHour int
	maxRequestsPerDay  int
}

type faucet struct {
	cfg                faucetConfig
	walletData         glb.WalletData
	mutex              sync.Mutex
	accountRequestList map[string][]time.Time
	addressRequestList map[string][]time.Time
}

const (
	defaultFaucetOutputAmount = 1_000_000
	defaultFaucetPort         = 9500
	defaultMaxRequestsPerHour = 5
	defaultMaxRequestsPerDay  = 10
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

	cmd.PersistentFlags().String("faucet.account", "a(0xe141252fd0ff04d12f9d485abfee4976e81e95cde436e8b9afde5b859d121e3e)", "faucet account")
	err = viper.BindPFlag("faucet.port", cmd.PersistentFlags().Lookup("faucet.account"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().String("faucet.priv_key", "", "faucet private key")
	err = viper.BindPFlag("faucet.priv_key", cmd.PersistentFlags().Lookup("faucet.priv_key"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().Uint64("faucet.max_requests_per_hour", defaultMaxRequestsPerHour, "maximum number of requests per hour")
	err = viper.BindPFlag("faucet.max_requests_per_hour", cmd.PersistentFlags().Lookup("faucet.max_requests_per_hour"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().Uint64("faucet.max_requests_per_day", defaultMaxRequestsPerDay, "maximum number of requests per day")
	err = viper.BindPFlag("faucet.max_requests_per_day", cmd.PersistentFlags().Lookup("faucet.max_requests_per_day"))
	glb.AssertNoError(err)
	return cmd
}

func readFaucetConfigIn(sub *viper.Viper, server bool) (ret faucetConfig) {
	if sub != nil {
		ret.port = sub.GetUint64("port")
		if server {
			ret.outputAmount = sub.GetUint64("output_amount")
			privateKeyStr := sub.GetString("priv_key")
			if len(privateKeyStr) > 0 {
				var err error
				ret.privKey, err = util.ED25519PrivateKeyFromHexString(privateKeyStr)
				glb.AssertNoError(err)
				ret.account = ledger.AddressED25519FromPrivateKey(ret.privKey)
				ret.redrawFromChain = false
			} else {
				ret.redrawFromChain = true
			}
		} else {
			ret.addr = sub.GetString("addr")
		}
		ret.maxRequestsPerHour = sub.GetInt("max_requests_per_hour")
		if ret.maxRequestsPerHour == 0 {
			ret.maxRequestsPerHour = defaultMaxRequestsPerHour
		}
		ret.maxRequestsPerDay = sub.GetInt("max_requests_per_day")
		if ret.maxRequestsPerDay == 0 {
			ret.maxRequestsPerDay = defaultMaxRequestsPerDay
		}
	} else {
		// get default values
		ret.port = viper.GetUint64("faucet.port")
		if server {
			ret.outputAmount = viper.GetUint64("faucet.output_amount")
			privateKeyStr := viper.GetString("faucet.priv_key")
			if len(privateKeyStr) > 0 {
				var err error
				ret.privKey, err = util.ED25519PrivateKeyFromHexString(privateKeyStr)
				glb.AssertNoError(err)
				ret.account = ledger.AddressED25519FromPrivateKey(ret.privKey)
				ret.redrawFromChain = false
			} else {
				ret.redrawFromChain = true
			}
		} else {
			ret.addr = viper.GetString("faucet.addr")
		}
		ret.maxRequestsPerHour = sub.GetInt("faucet.max_requests_per_hour")
		ret.maxRequestsPerDay = sub.GetInt("faucet.max_requests_per_day")
	}
	return
}

func displayFaucetConfig() faucetConfig {
	cfg := readFaucetConfigIn(viper.Sub("faucet"), true)
	glb.Infof("faucet configuration:")
	glb.Infof("     output amount:          %d", cfg.outputAmount)
	glb.Infof("     port:                   %d", cfg.port)
	glb.Infof("     private key:            %s", hex.EncodeToString(cfg.privKey))
	glb.Infof("     maximum number of requests per hour: %d, per day: %d", cfg.maxRequestsPerHour, cfg.maxRequestsPerDay)

	return cfg
}

func runFaucetCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()
	glb.Assertf(walletData.Sequencer != nil, "can't get own sequencer ID")
	glb.Infof("sequencer ID (funds source): %s", walletData.Sequencer.String())
	cfg := displayFaucetConfig()
	fct := &faucet{
		cfg:                cfg,
		walletData:         walletData,
		accountRequestList: make(map[string][]time.Time),
		addressRequestList: make(map[string][]time.Time),
	}
	if !cfg.redrawFromChain {
		funds := getAccountTotal(cfg.account)
		glb.Infof(" wallet funds: %d", funds)
		if funds < cfg.outputAmount {
			glb.Infof("Error not enough funds in waller")
			return
		}
	}
	fct.faucetServer()
}

const ownSequencerCmdFee = 50

func getAccountTotal(accountable ledger.Accountable) uint64 {
	var sum uint64
	outs, _, err := glb.GetClient().GetAccountOutputs(accountable)
	glb.AssertNoError(err)

	for _, o := range outs {
		sum += o.Output.Amount()
	}

	return sum
}

func (fct *faucet) redrawFromChain(targetLock ledger.Accountable) (string, *ledger.TransactionID) {
	glb.Infof("querying wallet's outputs..")
	walletOutputs, lrbid, err := glb.GetClient().GetAccountOutputs(fct.walletData.Account, func(_ *ledger.OutputID, o *ledger.Output) bool {
		return o.NumConstraints() == 2
	})
	if err != nil {
		glb.Infof("Error from GetAccountOutputs: %s", err.Error())
		return err.Error(), nil
	}

	glb.PrintLRB(lrbid)
	glb.Infof("will be using %d tokens as tag-along fee. Outputs in the wallet:", ownSequencerCmdFee)
	for i, o := range walletOutputs {
		glb.Infof("%d : %s : %s", i, o.ID.StringShort(), util.Th(o.Output.Amount()))
	}

	var tagAlongSeqID *ledger.ChainID
	feeAmount := getTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0. Fee-less option not supported yet")
	if feeAmount > 0 {
		tagAlongSeqID = GetTagAlongSequencerID()
		glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

		md, err := glb.GetClient().GetMilestoneData(*tagAlongSeqID)
		glb.AssertNoError(err)

		if md != nil && md.MinimumFee > feeAmount {
			feeAmount = ownSequencerCmdFee
		}
	}

	cmdConstr, err := commands.MakeSequencerWithdrawCommand(fct.cfg.outputAmount, targetLock.AsLock())
	if err != nil {
		glb.Infof("Error from MakeSequencerWithdrawCommand: %s", err.Error())
		return err.Error(), nil
	}

	transferData := txbuilder.NewTransferData(fct.walletData.PrivateKey, fct.walletData.Account, ledger.TimeNow()).
		WithAmount(feeAmount).
		WithTargetLock(ledger.ChainLockFromChainID(*fct.walletData.Sequencer)).
		MustWithInputs(walletOutputs...).
		WithSender().
		WithConstraint(cmdConstr)

	txBytes, err := txbuilder.MakeSimpleTransferTransaction(transferData)
	if err != nil {
		glb.Infof("Error from MakeSimpleTransferTransaction: %s", err.Error())
		return err.Error(), nil
	}

	txid, err := transaction.IDFromTransactionBytes(txBytes)
	glb.AssertNoError(err)

	err = glb.GetClient().SubmitTransaction(txBytes)
	if err != nil {
		glb.Infof("Error from SubmitTransaction: %s", err.Error())
		return err.Error(), nil
	}

	return "", &txid
}

func (fct *faucet) redrawFromAccount(targetLock ledger.Accountable) (string, *ledger.TransactionID) {
	funds := getAccountTotal(fct.cfg.account)
	glb.Infof(" wallet funds: %d", funds)
	if funds < fct.cfg.outputAmount {
		return "Error not enough funds in wallet", nil
	}

	glb.Infof("source is the wallet account: %s", fct.cfg.account.String())

	var tagAlongSeqID *ledger.ChainID
	feeAmount := getTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0. Fee-less option not supported yet")
	if feeAmount > 0 {
		tagAlongSeqID = GetTagAlongSequencerID()
		glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

		md, err := glb.GetClient().GetMilestoneData(*tagAlongSeqID)
		glb.AssertNoError(err)

		if md != nil && md.MinimumFee > feeAmount {
			feeAmount = ownSequencerCmdFee
		}
	}
	txCtx, err := glb.GetClient().TransferFromED25519Wallet(client.TransferFromED25519WalletParams{
		WalletPrivateKey: fct.cfg.privKey,
		TagAlongSeqID:    tagAlongSeqID,
		TagAlongFee:      feeAmount,
		Amount:           fct.cfg.outputAmount,
		Target:           targetLock.AsLock(),
	})

	if err != nil {
		return err.Error(), nil
	}

	glb.Assertf(txCtx != nil, "inconsistency: txCtx == nil")
	glb.Infof("transaction submitted successfully")

	return "", txCtx.TransactionID()
}

func _trimToLastDay(lst []time.Time) ([]time.Time, int) {
	ret := util.PurgeSlice(lst, func(when time.Time) bool {
		return time.Since(when) <= 24*time.Hour
	})
	lastHour := 0
	for _, when := range ret {
		if time.Since(when) <= time.Hour {
			lastHour++
		}
	}
	return ret, lastHour
}

func (fct *faucet) checkAndUpdateRequestTime(account string, addr string) bool {
	fct.mutex.Lock()
	defer fct.mutex.Unlock()

	var lastHour int

	lst, ok := fct.accountRequestList[account]
	if ok {
		lst, lastHour = _trimToLastDay(lst)
		if len(lst) >= fct.cfg.maxRequestsPerDay || lastHour >= fct.cfg.maxRequestsPerHour {
			return false
		}
		lst = append(lst, time.Now())
	} else {
		lst = []time.Time{time.Now()}
	}
	fct.accountRequestList[account] = lst

	lst, ok = fct.addressRequestList[addr]
	if ok {
		lst, lastHour = _trimToLastDay(lst)
		if len(lst) >= fct.cfg.maxRequestsPerDay || lastHour >= fct.cfg.maxRequestsPerHour {
			return false
		}
		lst = append(lst, time.Now())
	} else {
		lst = []time.Time{time.Now()}
	}
	fct.addressRequestList[addr] = lst
	return true
}

const faucetLogName = "faucet_requests.log"

func logRequest(account string, ipAddress string, funds uint64) error {
	// Open the log file in append mode, creating it if it doesn't exist
	file, err := os.OpenFile(faucetLogName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %v", err)
	}
	defer file.Close()

	// Create a logger
	logger := log.New(file, "", log.LstdFlags)

	// Log the request
	logger.Printf("Time: %s, Account: %s, IP: %s, Funds: %d\n", time.Now().Format(time.RFC3339), account, ipAddress, funds)
	return nil
}
func (fct *faucet) handler(w http.ResponseWriter, r *http.Request) {
	targetStr, ok := r.URL.Query()["addr"]
	if !ok || len(targetStr) != 1 {
		writeResponse(w, "wrong parameter 'addr' in request 'get_funds'")
		return
	}

	if !fct.checkAndUpdateRequestTime(targetStr[0], r.RemoteAddr) {
		glb.Infof("funds refused to send to %s (remote = %s)", targetStr[0], r.RemoteAddr)
		writeResponse(w, fmt.Sprintf("maximum %d requests per hour and %d per day are allowed", fct.cfg.maxRequestsPerHour, fct.cfg.maxRequestsPerDay))
		return
	}

	targetLock, err := ledger.AccountableFromSource(targetStr[0])
	if err != nil {
		glb.Infof("error from AccountableFromSource: %s", err.Error())
		writeResponse(w, err.Error())
		return
	}
	glb.Infof("sending funds to %s", targetStr[0])
	var result string
	var txid *ledger.TransactionID
	if fct.cfg.redrawFromChain {
		glb.Infof("redrawing from sequencer chain")
		result, txid = fct.redrawFromChain(targetLock)
	} else {
		glb.Infof("redrawing from account")
		result, txid = fct.redrawFromAccount(targetLock)
	}

	glb.Infof("requested faucet transfer of %s tokens to %s from sequencer %s (remote = %s)",
		util.Th(fct.cfg.outputAmount), targetLock.String(), fct.walletData.Sequencer.StringShort(), r.RemoteAddr)
	glb.Infof("             transaction %s (hex = %s)", txid.String(), txid.StringHex())
	writeResponse(w, result) // send ok

	logRequest(targetStr[0], r.RemoteAddr, fct.cfg.outputAmount)
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
	cfg := readFaucetConfigIn(viper.Sub("faucet"), false)

	faucetAddr := fmt.Sprintf("%s:%d", cfg.addr, cfg.port)

	glb.Infof("requesting funds from faucet at %s", faucetAddr)

	path := fmt.Sprintf(getFundsPath+"?addr=%s", walletData.Account.String())

	c := client.NewWithGoogleDNS(faucetAddr)
	answer, err := c.Get(path)
	glb.Infof("answer len: %d", len(answer))

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
