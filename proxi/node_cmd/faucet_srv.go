package node_cmd

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/commands"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const getFundsPath = "/"

type (
	faucetServerConfig struct {
		fromChain          bool
		amount             uint64
		port               uint64
		maxRequestsPerHour uint
		maxRequestsPerDay  uint
		bottom             uint64
	}

	faucetServer struct {
		cfg                faucetServerConfig
		walletData         glb.WalletData
		mutex              sync.Mutex
		accountRequestList map[string][]time.Time
		addressRequestList map[string][]time.Time
		client             *client.APIClient
	}
)

const (
	minAmount         = 1_000_000
	defaultFaucetPort = 9500
)

func initFaucetServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "faucet",
		Short: `starts a faucet server on the wallet`,
		Args:  cobra.NoArgs,
		Run:   runFaucetServerCmd,
	}
	return cmd
}

func runFaucetServerCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()
	glb.Infof("\nstarting Proxima faucet server..\n")
	walletData := glb.GetWalletData()
	glb.Assertf(walletData.Sequencer != nil, "can't get own sequencer id")
	glb.Assertf(glb.GetTagAlongFee() > 0, "tag-along amount not specified")
	glb.Assertf(glb.GetTagAlongSequencerID() != nil, "tag-along sequencer not specified")

	fct := &faucetServer{
		walletData:         walletData,
		accountRequestList: make(map[string][]time.Time),
		addressRequestList: make(map[string][]time.Time),
		client:             glb.GetClient(),
	}
	fct.readFaucetServerConfigIn()

	fct.displayFaucetConfig()

	if fct.cfg.fromChain {
		o, _, _, err := fct.client.GetChainOutput(*glb.GetOwnSequencerID())
		glb.AssertNoError(err)
		glb.Assertf(o.Output.TokenBalance() > ledger.L().ID.MinimumAmountOnSequencer+fct.cfg.amount,
			"not enough balance on own sequencer %s", fct.walletData.Sequencer.String())
	} else {
		_, _, _, err := fct.client.GetOutputsForAmount(walletData.Account, fct.cfg.amount+glb.GetTagAlongFee())
		glb.AssertNoError(err)
	}
	fct.run()
}

func (fct *faucetServer) readFaucetServerConfigIn() {
	sub := viper.Sub("faucet")
	glb.Assertf(sub != nil, "faucet server configuration is missing")
	fct.cfg.fromChain = !sub.GetBool("use_wallet_as_source")
	fct.cfg.port = sub.GetUint64("port")
	if fct.cfg.port == 0 {
		fct.cfg.port = defaultFaucetPort
	}
	fct.cfg.amount = sub.GetUint64("amount")
	glb.Assertf(fct.cfg.amount >= minAmount, "amount must be greater than %s", util.Th(minAmount))
	if fct.cfg.maxRequestsPerHour = sub.GetUint("max_requests_per_hour"); fct.cfg.maxRequestsPerHour == 0 {
		fct.cfg.maxRequestsPerHour = 1
	}
	if fct.cfg.maxRequestsPerDay = sub.GetUint("max_requests_per_day"); fct.cfg.maxRequestsPerDay == 0 {
		fct.cfg.maxRequestsPerDay = 1
	}
	fct.cfg.bottom = sub.GetUint64("bottom")
	if fct.cfg.bottom < fct.absoluteBottom() {
		fct.cfg.bottom = fct.absoluteBottom()
	}

	err := fct.checkBottom()
	glb.AssertNoError(err)
	return
}

func (fct *faucetServer) absoluteBottom() uint64 {
	if fct.cfg.fromChain {
		return ledger.L().ID.MinimumAmountOnSequencer + fct.cfg.amount
	}
	return fct.cfg.amount + glb.GetTagAlongFee()
}

func (fct *faucetServer) checkBottom() error {
	abs := fct.absoluteBottom()
	if fct.cfg.fromChain {
		o, _, _, err := fct.client.GetChainOutput(*glb.GetOwnSequencerID())
		if err != nil {
			return err
		}
		if o.Output.TokenBalance() < abs {
			return fmt.Errorf("not enough balance on own sequencer %s. Must be at least %s, got %s",
				fct.walletData.Sequencer.String(), util.Th(abs), util.Th(o.Output.TokenBalance()))
		}
	} else {
		balance, _, err := fct.client.GetNonChainBalance(fct.walletData.Account)
		if err != nil {
			return err
		}
		if balance < abs {
			return fmt.Errorf("not enough balance on source address %s. Must be at least %s, got %s",
				fct.walletData.Account.String(), util.Th(abs), util.Th(balance))
		}
	}
	return nil
}

func (fct *faucetServer) displayFaucetConfig() {
	walletBalance, lrbid, err := fct.client.GetNonChainBalance(fct.walletData.Account)
	glb.AssertNoError(err)
	glb.PrintLRB(lrbid)

	glb.Infof("faucet server configuration:")
	glb.Infof("     amount per request:       %s", util.Th(fct.cfg.amount))
	glb.Infof("     port:                     %d", fct.cfg.port)
	glb.Infof("     wallet address:           %s", fct.walletData.Account.String())
	glb.Infof("     wallet balance:           %s", util.Th(walletBalance))
	glb.Infof("     tag-along amount:         %d", glb.GetTagAlongFee())
	glb.Infof("     tag-along sequencer:      %s", glb.GetTagAlongSequencerID().String())
	glb.Infof("     bottom:                   %s", util.Th(fct.cfg.bottom))
	if fct.cfg.fromChain {
		chainOut, _, _, err := fct.client.GetChainOutput(*fct.walletData.Sequencer)
		glb.AssertNoError(err)
		glb.Infof("     funds will be drawn from: %s (balance %s)", fct.walletData.Sequencer.String(), util.Th(chainOut.Output.TokenBalance()))

	} else {
		glb.Infof("     funds will be drawn from: %s (balance %s)", fct.walletData.Account.String(), util.Th(walletBalance))
	}
	glb.Infof("     maximum number of requests per hour: %d, per day: %d", fct.cfg.maxRequestsPerHour, fct.cfg.maxRequestsPerDay)
}

func (fct *faucetServer) handler(w http.ResponseWriter, r *http.Request) {
	err := fct.checkBottom()
	if err != nil {
		glb.Infof("error from checkBottom: %s", err.Error())
		writeResponse(w, err.Error())
		return
	}

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
	var txid base.TransactionID
	var fromStr string
	if fct.cfg.fromChain {
		fromStr = "sequencer " + fct.walletData.Sequencer.StringShort()
		txid, err = fct.redrawFromChain(targetLock)
	} else {
		fromStr = "wallet address " + fct.walletData.Account.String()
		txid, err = fct.redrawFromAccount(targetLock)
	}

	if err == nil {
		glb.Infof("requested faucet transfer of %s tokens to %s from %s (remote = %s)",
			util.Th(fct.cfg.amount), targetLock.String(), fromStr, r.RemoteAddr)
		glb.Infof("             transaction %s (hex = %s)", txid.String(), txid.StringHex())
		writeResponse(w, "")
	} else {
		glb.Infof("failed faucet transfer of %s tokens to %s from %s (remote = %s): err = %v",
			util.Th(fct.cfg.amount), targetLock.String(), fromStr, r.RemoteAddr, err)
		writeResponse(w, err.Error())
	}

	logRequest(targetStr[0], r.RemoteAddr, fct.cfg.amount, err)
}

func (fct *faucetServer) redrawFromChain(targetLock ledger.Accountable) (base.TransactionID, error) {
	clnt := glb.GetClient()
	o, _, _, err := clnt.GetChainOutput(*glb.GetOwnSequencerID())
	if err != nil {
		return base.TransactionID{}, err
	}
	if o.Output.TokenBalance() < ledger.L().ID.MinimumAmountOnSequencer+fct.cfg.amount {
		return base.TransactionID{}, fmt.Errorf("not enough tokens on the sequencer %s", glb.GetOwnSequencerID().String())
	}
	walletOutputs, _, _, err := clnt.GetOutputsForAmount(fct.walletData.Account, glb.GetTagAlongFee())
	if err != nil {
		return base.TransactionID{}, err
	}
	withdrawCmd, err := commands.NewWithdrawCommandData(fct.cfg.amount, targetLock.AsLock())
	if err != nil {
		return base.TransactionID{}, err
	}
	// sending command to sequencer
	transferData := txbuilder.NewTransferData(fct.walletData.PrivateKey, fct.walletData.Account, ledger.TimeNow()).
		WithAmount(glb.GetTagAlongFee()).
		WithTargetLock(ledger.ChainLockFromChainID(*fct.walletData.Sequencer)).
		MustWithInputs(walletOutputs...).
		WithMessage(withdrawCmd)

	txBytes, err := txbuilder.MakeSimpleTransferTransaction(transferData)
	if err != nil {
		return base.TransactionID{}, err
	}
	tx, err := transaction.FromBytes(txBytes, transaction.MainTxValidationOptions...)
	if err != nil {
		return base.TransactionID{}, err
	}
	err = clnt.SubmitTransaction(txBytes)
	if err != nil {
		return base.TransactionID{}, err
	}
	return tx.ID(), nil
}

func (fct *faucetServer) redrawFromAccount(targetLock ledger.Accountable) (base.TransactionID, error) {
	txCtx, err := glb.GetClient().TransferFromED25519Wallet(client.TransferFromED25519WalletParams{
		WalletPrivateKey: fct.walletData.PrivateKey,
		TagAlongSeqID:    glb.GetTagAlongSequencerID(),
		TagAlongFee:      glb.GetTagAlongFee(),
		Amount:           fct.cfg.amount,
		Target:           targetLock.AsLock(),
	})

	if err != nil {
		return base.TransactionID{}, err
	}
	return txCtx.TransactionID(), nil
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

func (fct *faucetServer) checkAndUpdateRequestTime(account string, addr string) bool {
	fct.mutex.Lock()
	defer fct.mutex.Unlock()

	var lastHour int

	lst, ok := fct.accountRequestList[account]
	if ok {
		lst, lastHour = _trimToLastDay(lst)
		if len(lst) >= int(fct.cfg.maxRequestsPerDay) || lastHour >= int(fct.cfg.maxRequestsPerHour) {
			return false
		}
		lst = append(lst, time.Now())
	} else {
		lst = []time.Time{time.Now()}
	}
	fct.accountRequestList[account] = lst

	remoteHost, _, err := net.SplitHostPort(addr)
	if err != nil {
		remoteHost = addr
	}
	lst, ok = fct.addressRequestList[remoteHost]
	if ok {
		lst, lastHour = _trimToLastDay(lst)
		if len(lst) >= int(fct.cfg.maxRequestsPerDay) || lastHour >= int(fct.cfg.maxRequestsPerHour) {
			return false
		}
		lst = append(lst, time.Now())
	} else {
		lst = []time.Time{time.Now()}
	}
	fct.addressRequestList[remoteHost] = lst
	return true
}

const faucetLogName = "faucet_requests.log"

func logRequest(account string, remote string, funds uint64, err error) {
	// Open the log file in append mode, creating it if it doesn't exist
	file, err := os.OpenFile(faucetLogName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	glb.AssertNoError(err)
	defer func() { _ = file.Close() }()

	// Create a logger
	logger := log.New(file, "", log.LstdFlags)

	remoteHost, _, err := net.SplitHostPort(remote)
	if err != nil {
		remoteHost = remote
	}
	// Log the request
	if err == nil {
		logger.Printf("time: %s, to: %s, funds: %d, IP: %s, host: %s\n", time.Now().Format(time.RFC3339), account, funds, remote, remoteHost)
	} else {
		logger.Printf("time: %s, to: %s, funds: %d, IP: %s, host: %s, err: %v\n", time.Now().Format(time.RFC3339), account, funds, remote, remoteHost, err)
	}
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

func (fct *faucetServer) run() {
	http.HandleFunc(getFundsPath, fct.handler) // Route for the handler function
	sport := fmt.Sprintf(":%d", fct.cfg.port)
	glb.Infof("\nrunning proxi faucet server on %s. Press Ctrl-C to stop..\n", sport)
	glb.AssertNoError(http.ListenAndServe(sport, nil))
}
