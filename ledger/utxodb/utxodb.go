package utxodb

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
)

// UTXODB is a centralized ledger.Updatable with indexer and genesis faucet
// It is always final, does not have finality gadget nor the milestone chain
// It's primary purpose is testing the ledger
type UTXODB struct {
	store             global.Store
	state             *multistate.Updatable
	genesisChainID    base.ChainID
	supply            uint64
	genesisPrivateKey ed25519.PrivateKey
	genesisPublicKey  ed25519.PublicKey
	genesisAddress    ledger.SigLock
	faucetPrivateKey  ed25519.PrivateKey
	faucetAddress     ledger.SigLock
	trace             bool
	// for testing
	genesisOutput             *ledger.Output
	genesisStemOutput         *ledger.Output
	originDistributionTxBytes []byte
}

const (
	// for determinism
	deterministicSeed = "1234567890987654321"
)

func NewUTXODB(genesisPrivateKey ed25519.PrivateKey, trace ...bool) *UTXODB {
	genesisPubKey := ledger.L(0).GenesisControllerPublicKey
	genesisAddr := ledger.SigLockFromED25519PublicKey(genesisPubKey)
	util.Assertf(ledger.SigLockMatchesED25519PrivateKey(genesisAddr, genesisPrivateKey), "private key does not match controller address")

	stateStore := common.NewInMemoryKVStore()

	faucetPrivateKey := testutil.GetTestingPrivateKey(31415926535)
	faucetAddress := ledger.SigLockFromED25519PrivateKey(faucetPrivateKey)

	originChainID, genesisRoot := multistate.InitStateStoreFromGlobals(stateStore)
	rdr := multistate.MustNewSugaredReadableState(stateStore, genesisRoot)

	genesisOut, err := rdr.GetChainOutputWithID(originChainID)
	util.AssertNoError(err)

	genesisStemOut := rdr.GetStemOutput()

	distributionTxBytes := txbuilder_seq.MustDistributeInitialSupply(stateStore, genesisPrivateKey, []ledger.LockBalance{
		{Lock: faucetAddress, Balance: ledger.L(0).InitialSupply / 2, ChainOrigin: false},
	})

	updatable := multistate.MustNewUpdatable(stateStore, genesisRoot)
	_, err = updateValidateDebug(updatable, distributionTxBytes)
	util.AssertNoError(err)

	ret := &UTXODB{
		store:                     stateStore,
		state:                     updatable,
		genesisChainID:            originChainID,
		supply:                    ledger.L(0).InitialSupply,
		genesisPrivateKey:         genesisPrivateKey,
		genesisPublicKey:          genesisPubKey,
		genesisAddress:            genesisAddr,
		faucetPrivateKey:          faucetPrivateKey,
		faucetAddress:             faucetAddress,
		trace:                     len(trace) > 0 && trace[0],
		genesisOutput:             genesisOut.Output,
		genesisStemOutput:         genesisStemOut.Output,
		originDistributionTxBytes: distributionTxBytes,
	}
	return ret
}

func (u *UTXODB) Supply() uint64 {
	return u.supply
}

func (u *UTXODB) GenesisChainID() *base.ChainID {
	return &u.genesisChainID
}

func (u *UTXODB) Root() common.VCommitment {
	return u.state.Root()
}
func (u *UTXODB) StateReader() *multistate.Readable {
	return u.state.Readable()
}

func (u *UTXODB) SugaredStateReader() multistate.SugaredStateReader {
	return multistate.MakeSugared(u.state.Readable())
}

func (u *UTXODB) GenesisKeys() (ed25519.PrivateKey, ed25519.PublicKey) {
	return u.genesisPrivateKey, u.genesisPublicKey
}

func (u *UTXODB) GenesisControllerAddress() ledger.SigLock {
	return u.genesisAddress
}

func (u *UTXODB) FaucetAddress() ledger.SigLock {
	return u.faucetAddress
}

// AddTransaction validates transaction and updates ledger state and indexer
// Ledger state and indexer are on different DB transactions, so ledger state can
// succeed while indexer fails. In that case indexer can be updated from ledger state
func (u *UTXODB) AddTransaction(txBytes []byte, onValidationError ...func(ctx *transaction.Transaction, err error) error) error {
	var err error
	var tx *transaction.Transaction
	if u.trace {
		tx, err = updateValidateDebug(u.state, txBytes, onValidationError...)
	} else {
		tx, err = updateValidateNoDebug(u.state, txBytes)
	}
	if err != nil {
		return err
	}
	util.Assertf(!tx.IsBranchTransaction() || multistate.FetchLatestCommittedSlot(u.store) == tx.Slot(), "latestSlot == prevLatestSlot || latestSlot == tx.Slot()")
	util.Assertf(multistate.FetchEarliestSlot(u.store) == 0, "earliest slot in the UTXODB is expected to be 0")
	return nil
}

func (u *UTXODB) MakeTransactionFromFaucet(addr ledger.SigLock, amountPar ...uint64) ([]byte, error) {
	amount := ledger.DefaultStorageDeposit()
	if len(amountPar) > 0 && amountPar[0] > 0 {
		amount = amountPar[0]
	}
	faucetOutputs, err := u.StateReader().GetUTXOsInAccount(u.faucetAddress.AccountID())
	if err != nil {
		return nil, fmt.Errorf("UTXODB faucet: %v", err)
	}
	faucetInputs, err := ledger.ParseAndSortOutputData(faucetOutputs, nil)
	if err != nil {
		return nil, err
	}
	par := txbuilder.NewTransferData(u.faucetPrivateKey, nil, ledger.TimeNow()).
		WithAmount(amount, true).
		WithTargetLock(addr).
		MustWithInputs(faucetInputs...)

	txBytes, err := txbuilder.MakeTransferTransaction(par)
	if err != nil {
		return nil, fmt.Errorf("UTXODB faucet: %v", err)
	}

	return txBytes, nil
}

func (u *UTXODB) makeTransactionTokensFromFaucetMulti(addrs []ledger.SigLock, amounts ...uint64) ([]byte, error) {
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses")
	}
	amount := ledger.DefaultStorageDeposit()
	if len(amounts) > 0 && amounts[0] > 0 {
		amount = amounts[0]
	}
	if amount == 0 {
		return nil, fmt.Errorf("UTXODB faucet: wrong amount")
	}
	totalAmount := amount * uint64(len(addrs))
	faucetOutputs, err := u.StateReader().GetUTXOsInAccount(u.faucetAddress.AccountID())
	faucetInputs, inpAmount, ts, err := ledger.ParseAndSortOutputDataUpToAmount(faucetOutputs, totalAmount, nil)
	if err != nil {
		return nil, err
	}
	util.Assertf(inpAmount >= totalAmount, "inpAmount >= totalAmount")
	remainderAmount := inpAmount - totalAmount
	ts = ts.AddTicks(int(ledger.L(0).TransactionPace))
	txb := txbuilder.New()

	_, _, err = txb.ConsumeOutputsNoUnlock(faucetInputs...)
	if err != nil {
		return nil, err
	}
	for i := range faucetInputs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
			continue
		}
		if err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0); err != nil {
			return nil, err
		}
	}
	// remainder
	out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(remainderAmount)).WithLock(u.faucetAddress)
	})
	if _, err = txb.ProduceOutput(out); err != nil {
		return nil, err
	}
	// target outputs
	for _, a := range addrs {
		o := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(int64(amount)).WithLock(a)
		})
		if _, err := txb.ProduceOutput(o); err != nil {
			return nil, err
		}
	}
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(u.faucetPrivateKey)
	return txb.TransactionData.Bytes(), nil
}

func (u *UTXODB) TokensFromFaucet(addr ledger.SigLock, amount ...uint64) error {
	txBytes, err := u.MakeTransactionFromFaucet(addr, amount...)
	if err != nil {
		return err
	}

	return u.AddTransaction(txBytes, func(tx *transaction.Transaction, err error) error {
		if err != nil {
			return fmt.Errorf("Error: %v\n%s", err, tx.String())
		}
		return nil
	})
}

func (u *UTXODB) TokensFromFaucetMulti(addrs []ledger.SigLock, amount ...uint64) error {
	if len(addrs) == 0 {
		return nil
	}
	if len(addrs) <= 255 {
		txBytes, err := u.makeTransactionTokensFromFaucetMulti(addrs, amount...)
		if err != nil {
			return err
		}
		return u.AddTransaction(txBytes, func(tx *transaction.Transaction, err error) error {
			if err != nil {
				return fmt.Errorf("Error: %v\n%s", err, tx.String())
			}
			return nil
		})
	}
	if err := u.TokensFromFaucetMulti(addrs[:255], amount...); err != nil {
		return err
	}
	return u.TokensFromFaucetMulti(addrs[255:], amount...)
}

func (u *UTXODB) GenerateAddress(n int) (ed25519.PrivateKey, ed25519.PublicKey, ledger.SigLock) {
	var u32 [4]byte
	binary.BigEndian.PutUint32(u32[:], uint32(n))
	seed := blake2b.Sum256(common.Concat([]byte(deterministicSeed), u32[:]))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)
	addr := ledger.SigLockFromED25519PublicKey(pub)
	return priv, pub, addr
}

func (u *UTXODB) GenerateAddresses(startIndex int, n int) ([]ed25519.PrivateKey, []ed25519.PublicKey, []ledger.SigLock) {
	retPriv := make([]ed25519.PrivateKey, n)
	retPub := make([]ed25519.PublicKey, n)
	retAddr := make([]ledger.SigLock, n)
	util.Assertf(n > 0, "number of addresses must be positive")
	for i := 0; i < n; i++ {
		retPriv[i], retPub[i], retAddr[i] = u.GenerateAddress(startIndex + i)
	}
	return retPriv, retPub, retAddr
}

func (u *UTXODB) GenerateAddressesWithFaucetAmount(startIndex int, n int, amount uint64) ([]ed25519.PrivateKey, []ed25519.PublicKey, []ledger.SigLock) {
	retPriv, retPub, retAddr := u.GenerateAddresses(startIndex, n)
	err := u.TokensFromFaucetMulti(retAddr, amount)
	util.AssertNoError(err)
	return retPriv, retPub, retAddr
}

func (u *UTXODB) GenerateUTXOsWithFaucetAmount(addr ledger.SigLock, n int, amount uint64) []*ledger.OutputWithID {
	util.Assertf(n > 0, "number of addresses must be positive")
	for i := 0; i < n; i++ {
		err := u.TokensFromFaucet(addr, amount)
		util.AssertNoError(err)

	}
	util.Assertf(u.Balance(addr) == amount*uint64(n), "u.Balance(addr)==amount*uint64(n)")

	rdr := multistate.MakeSugared(u.StateReader())
	ret, err := rdr.GetOutputsForAccount(addr.AccountID())
	util.AssertNoError(err)
	util.Assertf(len(ret) == n, "len(ret)!=n")
	return ret
}

func (u *UTXODB) MakeTransferInputData(privKey ed25519.PrivateKey, sourceAccount ledger.Accountable, ts base.LedgerTime, desc ...bool) (*txbuilder.TransferData, error) {
	if ts == base.NilLedgerTime {
		ts = ledger.TimeNow()
	}
	ret := txbuilder.NewTransferData(privKey, sourceAccount, ts)

	switch addr := ret.SourceAccount.(type) {
	case ledger.SigLock:
		if err := u.makeTransferInputsED25519(ret, desc...); err != nil {
			return nil, err
		}
		return ret, nil
	case ledger.ChainLock:
		if err := u.makeTransferDataChainLock(ret, addr, desc...); err != nil {
			return nil, err
		}
	default:
		panic(fmt.Sprintf("wrong source account type %T", sourceAccount))
	}
	return ret, nil
}

func (u *UTXODB) makeTransferInputsED25519(par *txbuilder.TransferData, desc ...bool) error {
	outsData, err := u.StateReader().GetUTXOsInAccount(par.SourceAccount.AccountID())
	if err != nil {
		return err
	}
	outs, err := ledger.ParseAndSortOutputData(outsData, func(oid *base.OutputID, o *ledger.Output) bool {
		_, idx := o.ChainConstraint()
		return idx == 0xff && o.Lock().Name() == ledger.SigLockName
	}, desc...)
	if err != nil {
		return err
	}
	par.MustWithInputs(outs...)
	return nil
}

func (u *UTXODB) makeTransferDataChainLock(par *txbuilder.TransferData, chainLock ledger.ChainLock, desc ...bool) error {
	outChain, outs, err := txbuilder.GetChainAccount(chainLock.ChainID(), u.StateReader(), desc...)
	if err != nil {
		return err
	}
	par.MustWithInputs(outs...).
		WithChainOutput(outChain)
	return nil
}

func (u *UTXODB) TransferTokensReturnTx(privKey ed25519.PrivateKey, targetLock ledger.Lock, amount uint64) (*transaction.Transaction, error) {
	txBytes, err := u.transferTokens(privKey, targetLock, amount)
	if err != nil {
		return nil, err
	}
	return transaction.FromBytesMainChecksWithOpt(txBytes)
}

func (u *UTXODB) transferTokens(privKey ed25519.PrivateKey, targetLock ledger.Lock, amount uint64) ([]byte, error) {
	par, err := u.MakeTransferInputData(privKey, nil, base.NilLedgerTime)
	if err != nil {
		return nil, err
	}
	par.WithAmount(amount).
		WithTargetLock(targetLock)
	txBytes, err := txbuilder.MakeTransferTransaction(par)
	if err != nil {
		return nil, err
	}
	return txBytes, u.AddTransaction(txBytes, func(tx *transaction.Transaction, err error) error {
		if err != nil {
			return fmt.Errorf("Error: %v\n%s", err, tx.String())
		}
		return nil
	})
}

func (u *UTXODB) TransferTokens(privKey ed25519.PrivateKey, targetLock ledger.Lock, amount uint64) error {
	_, err := u.transferTokens(privKey, targetLock, amount)
	return err
}

func (u *UTXODB) account(addr ledger.Accountable) (uint64, int) {
	outs, err := u.StateReader().GetUTXOsInAccount(addr.AccountID())
	util.AssertNoError(err)
	balance := uint64(0)
	outs1, err := ledger.ParseAndSortOutputData(outs, nil)
	util.AssertNoError(err)

	for _, o := range outs1 {
		balance += o.Output.TokenBalance()
	}
	return balance, len(outs1)
}

// Balance returns balance of address unlockable at timestamp ts, if provided. Otherwise, all outputs taken
// For chains, this does not include te chain-output itself
func (u *UTXODB) Balance(addr ledger.Accountable) uint64 {
	ret, _ := u.account(addr)
	return ret
}

// BalanceOnChain returns balance locked in chain and separately balance on chain output
func (u *UTXODB) BalanceOnChain(chainID base.ChainID) (uint64, uint64, error) {
	outChain, outs, err := txbuilder.GetChainAccount(chainID, u.StateReader())
	if err != nil {
		return 0, 0, err
	}
	amount := uint64(0)
	for _, odata := range outs {
		amount += odata.Output.TokenBalance()
	}
	return amount, outChain.Output.TokenBalance(), nil
}

// NumUTXOs returns number of outputs in the address
func (u *UTXODB) NumUTXOs(addr ledger.Accountable) int {
	_, ret := u.account(addr)
	return ret
}

func (u *UTXODB) DoTransferTx(par *txbuilder.TransferData) ([]byte, error) {
	txBytes, err := txbuilder.MakeTransferTransaction(par)
	if err != nil {
		return nil, err
	}
	return txBytes, u.AddTransaction(txBytes, func(tx *transaction.Transaction, err error) error {
		if err != nil {
			return fmt.Errorf("Error: %v\n%s", err, tx.String())
		}
		return nil
	})
}

func (u *UTXODB) DoTransferOutputs(par *txbuilder.TransferData) ([]*ledger.OutputWithID, error) {
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(par)
	if err != nil {
		return nil, err
	}
	if err = u.AddTransaction(txBytes, func(tx *transaction.Transaction, err error) error {
		if err != nil {
			return fmt.Errorf("Error: %v\n%s", err, tx.String())
		}
		return nil
	}); err != nil {
		return nil, err
	}
	tx, err := transaction.Parse(txBytes)
	if err != nil {
		return nil, err
	}
	return tx.ProducedOutputs(), nil
}

func (u *UTXODB) DoTransfer(par *txbuilder.TransferData) error {
	_, err := u.DoTransferTx(par)
	return err
}

func (u *UTXODB) SendOutput(privKey ed25519.PrivateKey, o *ledger.Output, ts base.LedgerTime) error {
	fromAccount := ledger.SigLockFromED25519PrivateKey(privKey)
	ins := make([]*ledger.OutputWithID, 0)
	sum := uint64(0)
	sendAmount := o.TokenBalance()
	txb := txbuilder.New()
	var err1 error

	err := u.SugaredStateReader().IterateOutputsForAccount(fromAccount, func(oid base.OutputID, o *ledger.Output) bool {
		if o.NumConstraints() > 2 || o.Lock().Name() != ledger.SigLockName {
			return true
		}
		ins = append(ins, &ledger.OutputWithID{
			Output: o,
			ID:     oid,
		})
		sum += o.TokenBalance()
		var idx byte
		idx, err1 = txb.ConsumeOutput(o, oid)
		if err1 != nil {
			return false
		}
		if idx == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			_ = txb.PutUnlockReference(idx, ledger.ConstraintIndexLock, 0)
		}
		return sum < sendAmount
	})
	if err != nil || err1 != nil {
		return err1
	}
	if sum < sendAmount {
		return fmt.Errorf("not enough funds")
	}
	if _, err = txb.ProduceOutput(o); err != nil {
		return err
	}
	if sum >= sendAmount {
		_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(sum - sendAmount)
			o.WithLock(fromAccount)
		}))
		if err != nil {
			return err
		}
	}
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(privKey)
	txBytes, _, txString, err := txb.BytesWithValidation()
	if err != nil {
		return fmt.Errorf("error: %v\n%s", err, txString)
	}
	err = u.AddTransaction(txBytes)
	util.AssertNoError(err)
	return nil
}

func (u *UTXODB) MakeNewChain(amount uint64, privateKey ed25519.PrivateKey, chainController ledger.Lock, timestamp ...base.LedgerTime) (*ledger.OutputWithChainID, error) {
	ts := ledger.TimeNow()
	if len(timestamp) > 0 {
		ts = timestamp[0]
	}

	par, err := u.MakeTransferInputData(privateKey, nil, ts)
	if err != nil {
		return nil, err
	}
	par.WithAmount(amount, true).
		WithTargetLock(chainController)
	par.WithConstraint(ledger.NewChainOrigin(ts.Slot, par.Amount))

	outs, err := u.DoTransferOutputs(par)
	if err != nil {
		return nil, err
	}
	outs = util.PurgeSlice(outs, func(o *ledger.OutputWithID) bool {
		_, idx := o.Output.ChainConstraint()
		return idx != 0xff
	})
	util.Assertf(len(outs) == 1, "len(outs)>0")

	chainData, ok := ledger.ExtractChainData(outs[0].Output, outs[0].ID)
	if !ok {
		return nil, fmt.Errorf("error extracting chain data")
	}

	return &ledger.OutputWithChainID{
		OutputWithID:        *outs[0],
		ChainConstraintData: chainData,
	}, nil
}

func (u *UTXODB) TxFullContextFromBytes(txBytes []byte) (*transaction.Transaction, error) {
	tx, err := transaction.Parse(txBytes)
	if err != nil {
		return nil, err
	}
	err = tx.SetFullContext(tx.InputLoaderByIndex(u.state.Readable().GetUTXO))
	if err != nil {
		return nil, err
	}
	return tx, nil
}

//func (u *UTXODB) TxToLines(txBytes []byte, prefix ...string) *lines.Lines {
//	ctx, err := u.TxFullContextFromBytes(txBytes)
//	if err != nil {
//		return lines.New(prefix...).Add("error: %v", err)
//	}
//	return ctx.Lines(prefix...)
//}

func (u *UTXODB) TxToLinesSource(txBytes []byte, prefix ...string) *lines.Lines {
	tx, err := u.TxFullContextFromBytes(txBytes)
	if err != nil {
		return lines.New(prefix...).Add("error: %v", err)
	}
	return tx.Lines(nil, prefix...)
}

func (u *UTXODB) TxToSource(txBytes []byte) string {
	return u.TxToLinesSource(txBytes).String()
}

// CreateChainOrigin takes all tokens from controller address and puts them on the chain output
func (u *UTXODB) CreateChainOrigin(controllerPrivateKey ed25519.PrivateKey, ts base.LedgerTime, initAmount ...uint64) (*ledger.OutputWithChainID, error) {
	controllerAddress := ledger.SigLockFromED25519PrivateKey(controllerPrivateKey)
	var amount uint64
	if len(initAmount) > 0 {
		amount = initAmount[0]
	} else {
		amount = u.Balance(controllerAddress)
	}
	td, err := u.MakeTransferInputData(controllerPrivateKey, controllerAddress, ts)
	if err != nil {
		return nil, err
	}
	outs, err := u.DoTransferOutputs(td.
		WithAmount(amount).
		WithTargetLock(controllerAddress).
		WithConstraint(ledger.NewChainOrigin(ts.Slot, amount)),
	)
	if err != nil {
		return nil, err
	}
	chains, err := ledger.FilterChainOutputs(outs)
	if err != nil {
		return nil, err
	}
	return chains[0], nil

}

func (u *UTXODB) OriginDistributionTransactionString() string {
	genesisStemOutputID := base.GenesisStemOutputID()
	genesisOutputID := base.GenesisOutputID()

	return transaction.ParseBytesToString(u.originDistributionTxBytes, func(oid base.OutputID) ([]byte, bool) {
		switch oid {
		case genesisOutputID:
			return u.genesisOutput.Bytes(), true
		case genesisStemOutputID:
			return u.genesisStemOutput.Bytes(), true
		}
		panic("OriginDistributionTransactionString: inconsistency")
	})
}

func (u *UTXODB) FaucetBalance() uint64 {
	return u.Balance(u.FaucetAddress())
}

func (u *UTXODB) TxStringFromBytes(txBytes []byte) string {
	tx, err := transaction.Parse(txBytes)
	if err != nil {
		return err.Error()
	}
	return tx.String()
}
