package node_cmd

// DISABLED — `proxi node faucet` long-running server. Built on the
// (now also disabled) glb.TransferFromED25519Wallet +
// glb.MakeSendOutputTransaction wallet recipes, which use
// ledger/txbuilder + the ledger.L() singleton. Registration was
// already commented off in node_cmd.go; the body is commented off
// here in lockstep with proxi/glb/wallet_recipes.go and the
// matching client (faucet_get.go). Revive together when the faucet
// is ported to the wasm-style txbuildercore pipeline.

/*
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
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// TODO implement receiving funds as delegated output

const getFundsPath = "/"

type (
	faucetServerConfig struct {
		fromChain          bool
		amount             uint64
		port               uint64
		maxRequestsPerHour uint
		maxRequestsPerDay  uint
		maxRequestsPerAddr uint
		bottom             uint64
	}

	faucetServer struct {
		cfg                   faucetServerConfig
		walletData            glb.WalletData
		mutex                 sync.Mutex
		accountRequestList    map[string][]time.Time
		addressRequestList    map[string][]time.Time
		addressRequestCount   map[string]uint
		client                *client.APIClient
		withdrawTagAlongFee   uint64        // fee for withdrawing from own chain (fromChain mode)
		transferTagAlongFee   uint64        // fee for transfer from wallet
		transferTagAlongSeqID *base.ChainID // sequencer ID for wallet transfer tag-along
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
	glb.Infof("\nstarting Proxima faucet server on the wallet..\n")
	walletData := glb.GetWalletData()
	glb.Assertf(walletData.Sequencer != nil, "can't get own sequencer id")

	fct := &faucetServer{
		walletData:          walletData,
		accountRequestList:  make(map[string][]time.Time),
		addressRequestList:  make(map[string][]time.Time),
		addressRequestCount: make(map[string]uint),
		client:              glb.GetClient(),
	}
	fct.readFaucetServerConfigIn()

	// Get tag-along fees at startup (don't prompt interactively for server)
	// For withdrawing from own chain
	withdrawFee, err := glb.GetRequiredTagAlongFee(*walletData.Sequencer)
	glb.AssertNoError(err)
	fct.withdrawTagAlongFee = withdrawFee

	// For wallet transfers
	fct.transferTagAlongSeqID = glb.GetTagAlongSequencerID()
	glb.Assertf(fct.transferTagAlongSeqID != nil, "tag-along sequencer not specified")
	transferFee, err := glb.GetRequiredTagAlongFee(*fct.transferTagAlongSeqID)
	glb.AssertNoError(err)
	fct.transferTagAlongFee = transferFee

	fct.displayFaucetConfig()

	if fct.cfg.fromChain {
		o, _, err := fct.client.GetChainOutput(*glb.GetOwnSequencerID())
		glb.AssertNoError(err)
		glb.Assertf(o.Output.TokenBalance() > fct.cfg.amount,
			"not enough balance on own sequencer %s", fct.walletData.Sequencer.String())
	} else {
		needed := fct.cfg.amount + fct.transferTagAlongFee
		res, err := fct.client.GetOutputsForControllerID(walletData.Account.ControllerID(), client.GetOutputsParams{
			LockType:  api.GetOutputsLockTypeSigLock,
			Chained:   client.NonChainedOnly(),
			ForAmount: needed,
		})
		glb.AssertNoError(err)
		glb.Assertf(res.AvailableAmount >= needed, "not enough tokens on wallet: have %s, need %s", util.Th(res.AvailableAmount), util.Th(needed))
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
	if fct.cfg.maxRequestsPerAddr = sub.GetUint("max_requests_per_addr"); fct.cfg.maxRequestsPerAddr == 0 {
		fct.cfg.maxRequestsPerAddr = 2
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
		return fct.cfg.amount
	}
	return fct.cfg.amount + fct.transferTagAlongFee
}

func (fct *faucetServer) checkBottom() error {
	abs := fct.absoluteBottom()
	if fct.cfg.fromChain {
		o, _, err := fct.client.GetChainOutput(*glb.GetOwnSequencerID())
		if err != nil {
			return err
		}
		if o.Output.TokenBalance() < abs {
			return fmt.Errorf("not enough balance on own sequencer %s. Must be at least %s, got %s",
				fct.walletData.Sequencer.String(), util.Th(abs), util.Th(o.Output.TokenBalance()))
		}
	} else {
		res, err := fct.client.GetOutputsForControllerID(fct.walletData.Account.ControllerID(), client.GetOutputsParams{
			LockType: api.GetOutputsLockTypeSigLock,
			Chained:  client.NonChainedOnly(),
		})
		if err != nil {
			return err
		}
		if res.AvailableAmount < abs {
			return fmt.Errorf("not enough balance on source address %s. Must be at least %s, got %s",
				fct.walletData.Account.String(), util.Th(abs), util.Th(res.AvailableAmount))
		}
	}
	return nil
}

func (fct *faucetServer) displayFaucetConfig() {
	res, err := fct.client.GetOutputsForControllerID(fct.walletData.Account.ControllerID(), client.GetOutputsParams{
		LockType: api.GetOutputsLockTypeSigLock,
		Chained:  client.NonChainedOnly(),
	})
	glb.AssertNoError(err)
	walletBalance := res.AvailableAmount
	glb.PrintLRB(&res.LRBID)

	glb.Infof("faucet server configuration:")
	glb.Infof("     amount per request:       %s", util.Th(fct.cfg.amount))
	glb.Infof("     port:                     %d", fct.cfg.port)
	glb.Infof("     wallet address:           %s", fct.walletData.Account.String())
	glb.Infof("     wallet balance:           %s", util.Th(walletBalance))
	if fct.cfg.fromChain {
		glb.Infof("     withdraw tag-along fee:   %s (to own sequencer)", util.Th(fct.withdrawTagAlongFee))
	} else {
		glb.Infof("     transfer tag-along fee:   %s", util.Th(fct.transferTagAlongFee))
		glb.Infof("     tag-along sequencer:      %s", fct.transferTagAlongSeqID.String())
	}
	glb.Infof("     bottom:                   %s", util.Th(fct.cfg.bottom))
	if fct.cfg.fromChain {
		chainOut, _, err := fct.client.GetChainOutput(*fct.walletData.Sequencer)
		glb.AssertNoError(err)
		glb.Infof("     funds will be drawn from: %s (balance %s)", fct.walletData.Sequencer.String(), util.Th(chainOut.Output.TokenBalance()))

	} else {
		glb.Infof("     funds will be drawn from: %s (balance %s)", fct.walletData.Account.String(), util.Th(walletBalance))
	}
	glb.Infof("     maximum number of requests per hour: %d, per day: %d, per address: %d",
		fct.cfg.maxRequestsPerHour, fct.cfg.maxRequestsPerDay, fct.cfg.maxRequestsPerAddr)
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
	nReq := fct.addressRequestCount[targetStr[0]]
	if nReq >= fct.cfg.maxRequestsPerAddr {
		glb.Infof("funds refused to send to %s (remote = %s)", targetStr[0], r.RemoteAddr)
		writeResponse(w, "maximum number of requests exceeded")
		return
	}
	fct.addressRequestCount[targetStr[0]] = nReq + 1

	if !fct.checkAndUpdateRequestTime(targetStr[0], r.RemoteAddr) {
		glb.Infof("funds refused to send to %s (remote = %s)", targetStr[0], r.RemoteAddr)
		writeResponse(w, fmt.Sprintf("maximum %d requests per hour and %d per day are allowed", fct.cfg.maxRequestsPerHour, fct.cfg.maxRequestsPerDay))
		return
	}

	targetLock, err := ledger.ControllerFromSource(targetStr[0])
	if err != nil {
		glb.Infof("error from ControllerFromSource: %s", err.Error())
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

func (fct *faucetServer) redrawFromChain(targetLock ledger.Controller) (base.TransactionID, error) {
	clnt := glb.GetClient()
	o, _, err := clnt.GetChainOutput(*glb.GetOwnSequencerID())
	if err != nil {
		return base.TransactionID{}, err
	}
	if o.Output.TokenBalance() < fct.cfg.amount {
		return base.TransactionID{}, fmt.Errorf("not enough tokens on the sequencer %s", glb.GetOwnSequencerID().String())
	}

	tagAlongOut := txbuilder_seq.NewWithdrawRequestOutput(*fct.walletData.Sequencer, fct.walletData.Account, fct.withdrawTagAlongFee, fct.cfg.amount, targetLock)
	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(12)
	}
	txBytes, txid, txString, err := glb.MakeSendOutputTransaction(tagAlongOut, fct.walletData.PrivateKey, ts)
	if err != nil {
		if txString != "" {
			err = fmt.Errorf("error %v\n----------- failing tx ------------\n%s", err, txString)
		}
		return base.TransactionID{}, err
	}
	glb.Verbosef("---------------- withdraw tx -----------------\n%s", txString)

	err = clnt.SubmitTransaction(txBytes)
	if err != nil {
		return base.TransactionID{}, err
	}
	return txid, nil
}

func (fct *faucetServer) redrawFromAccount(targetLock ledger.Controller) (base.TransactionID, error) {
	txCtx, err := glb.TransferFromED25519Wallet(glb.TransferFromED25519WalletParams{
		WalletPrivateKey: fct.walletData.PrivateKey,
		TagAlongSeqID:    fct.transferTagAlongSeqID,
		TagAlongFee:      fct.transferTagAlongFee,
		Amount:           fct.cfg.amount,
		Target:           targetLock,
	})

	if err != nil {
		return base.TransactionID{}, err
	}
	return txCtx.ID(), nil
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
*/
