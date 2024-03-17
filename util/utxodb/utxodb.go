package utxodb

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/lunfardo314/proxima/util/txutils"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
)

// UTXODB is a centralized ledger.Updatable with indexer and genesis faucet
// It is always final, does not have finality gadget nor the milestone chain
// It is mainly used for testing of constraints
type UTXODB struct {
	state             *multistate.Updatable
	genesisChainID    ledger.ChainID
	supply            uint64
	genesisPrivateKey ed25519.PrivateKey
	genesisPublicKey  ed25519.PublicKey
	genesisAddress    ledger.AddressED25519
	faucetPrivateKey  ed25519.PrivateKey
	faucetAddress     ledger.AddressED25519
	trace             bool
	// for testing
	genesisOutput             *ledger.Output
	genesisStemOutput         *ledger.Output
	originDistributionTxBytes []byte
}

const (
	// for determinism
	deterministicSeed       = "1234567890987654321"
	TokensFromFaucetDefault = uint64(1_000_000)
)

func NewUTXODB(genesisPrivateKey ed25519.PrivateKey, trace ...bool) *UTXODB {
	genesisPubKey := ledger.L().ID.GenesisControllerPublicKey
	genesisAddr := ledger.AddressED25519FromPublicKey(genesisPubKey)
	util.Assertf(ledger.AddressED25519MatchesPrivateKey(genesisAddr, genesisPrivateKey), "private key does not match controller address")

	stateStore := common.NewInMemoryKVStore()

	faucetPrivateKey := testutil.GetTestingPrivateKey(31415926535)
	faucetAddress := ledger.AddressED25519FromPrivateKey(faucetPrivateKey)

	initLedgerParams := ledger.L().ID
	originChainID, genesisRoot := multistate.InitStateStore(*initLedgerParams, stateStore)
	rdr := multistate.MustNewSugaredReadableState(stateStore, genesisRoot)

	genesisOut, err := rdr.GetChainOutput(&originChainID)
	util.AssertNoError(err)

	genesisStemOut := rdr.GetStemOutput()

	distributionTxBytes := txbuilder.MustDistributeInitialSupply(stateStore, genesisPrivateKey, []ledger.LockBalance{
		{faucetAddress, ledger.L().ID.InitialSupply / 2},
	})

	updatable := multistate.MustNewUpdatable(stateStore, genesisRoot)
	_, err = updateValidateDebug(updatable, distributionTxBytes)
	util.AssertNoError(err)

	ret := &UTXODB{
		state:                     updatable,
		genesisChainID:            originChainID,
		supply:                    initLedgerParams.InitialSupply,
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

func (u *UTXODB) StateIdentityData() *ledger.IdentityData {
	return ledger.MustLedgerIdentityDataFromBytes(u.StateReader().MustLedgerIdentityBytes())
}

func (u *UTXODB) GenesisChainID() *ledger.ChainID {
	return &u.genesisChainID
}

func (u *UTXODB) Root() common.VCommitment {
	return u.state.Root()
}
func (u *UTXODB) StateReader() *multistate.Readable {
	return u.state.Readable()
}

func (u *UTXODB) GenesisKeys() (ed25519.PrivateKey, ed25519.PublicKey) {
	return u.genesisPrivateKey, u.genesisPublicKey
}

func (u *UTXODB) GenesisControllerAddress() ledger.AddressED25519 {
	return u.genesisAddress
}

func (u *UTXODB) FaucetAddress() ledger.AddressED25519 {
	return u.faucetAddress
}

// AddTransaction validates transaction and updates ledger state and indexer
// Ledger state and indexer are on different DB transactions, so ledger state can
// succeed while indexer fails. In that case indexer can be updated from ledger state
func (u *UTXODB) AddTransaction(txBytes []byte, onValidationError ...func(ctx *transaction.TxContext, err error) error) error {
	var err error
	if u.trace {
		_, err = updateValidateDebug(u.state, txBytes, onValidationError...)
	} else {
		_, err = updateValidateNoDebug(u.state, txBytes)
	}
	if err != nil {
		return err
	}
	return nil
}

func (u *UTXODB) MakeTransactionFromFaucet(addr ledger.AddressED25519, amountPar ...uint64) ([]byte, error) {
	amount := TokensFromFaucetDefault
	if len(amountPar) > 0 && amountPar[0] > 0 {
		amount = amountPar[0]
	}
	faucetOutputs, err := u.StateReader().GetUTXOsLockedInAccount(u.faucetAddress.AccountID())
	if err != nil {
		return nil, fmt.Errorf("UTXODB faucet: %v", err)
	}
	faucetInputs, err := txutils.ParseAndSortOutputData(faucetOutputs, nil)
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

func (u *UTXODB) makeTransactionTokensFromFaucetMulti(addrs []ledger.AddressED25519, amounts ...uint64) ([]byte, error) {
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses")
	}
	amount := TokensFromFaucetDefault
	if len(amounts) > 0 && amounts[0] > 0 {
		amount = amounts[0]
	}
	if amount == 0 {
		return nil, fmt.Errorf("UTXODB faucet: wrong amount")
	}
	totalAmount := amount * uint64(len(addrs))
	faucetOutputs, err := u.StateReader().GetUTXOsLockedInAccount(u.faucetAddress.AccountID())
	faucetInputs, inpAmount, ts, err := txutils.ParseAndSortOutputDataUpToAmount(faucetOutputs, totalAmount, nil)
	if err != nil {
		return nil, err
	}
	util.Assertf(inpAmount >= totalAmount, "inpAmount >= totalAmount")
	remainderAmount := inpAmount - totalAmount
	ts = ts.AddTicks(ledger.TransactionPace())
	txb := txbuilder.NewTransactionBuilder()

	_, _, err = txb.ConsumeOutputs(faucetInputs...)
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
	out := ledger.NewOutput(func(o *ledger.Output) {
		o.WithAmount(remainderAmount).WithLock(u.faucetAddress)
	})
	if _, err = txb.ProduceOutput(out); err != nil {
		return nil, err
	}
	// target outputs
	for _, a := range addrs {
		o := ledger.NewOutput(func(o *ledger.Output) {
			o.WithAmount(amount).WithLock(a)
		})
		if _, err := txb.ProduceOutput(o); err != nil {
			return nil, err
		}
	}
	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = txb.InputCommitment()
	txb.SignED25519(u.faucetPrivateKey)
	return txb.TransactionData.Bytes(), nil
}

func (u *UTXODB) TokensFromFaucet(addr ledger.AddressED25519, amount ...uint64) error {
	txBytes, err := u.MakeTransactionFromFaucet(addr, amount...)
	if err != nil {
		return err
	}

	return u.AddTransaction(txBytes, func(ctx *transaction.TxContext, err error) error {
		if err != nil {
			return fmt.Errorf("Error: %v\n%s", err, ctx.String())
		}
		return nil
	})
}

func (u *UTXODB) TokensFromFaucetMulti(addrs []ledger.AddressED25519, amount ...uint64) error {
	if len(addrs) == 0 {
		return nil
	}
	if len(addrs) <= 255 {
		txBytes, err := u.makeTransactionTokensFromFaucetMulti(addrs, amount...)
		if err != nil {
			return err
		}
		return u.AddTransaction(txBytes, func(ctx *transaction.TxContext, err error) error {
			if err != nil {
				return fmt.Errorf("Error: %v\n%s", err, ctx.String())
			}
			return nil
		})
	}
	if err := u.TokensFromFaucetMulti(addrs[:255], amount...); err != nil {
		return err
	}
	return u.TokensFromFaucetMulti(addrs[255:], amount...)
}

func (u *UTXODB) GenerateAddress(n int) (ed25519.PrivateKey, ed25519.PublicKey, ledger.AddressED25519) {
	var u32 [4]byte
	binary.BigEndian.PutUint32(u32[:], uint32(n))
	seed := blake2b.Sum256(common.Concat([]byte(deterministicSeed), u32[:]))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)
	addr := ledger.AddressED25519FromPublicKey(pub)
	return priv, pub, addr
}

func (u *UTXODB) GenerateAddresses(startIndex int, n int) ([]ed25519.PrivateKey, []ed25519.PublicKey, []ledger.AddressED25519) {
	retPriv := make([]ed25519.PrivateKey, n)
	retPub := make([]ed25519.PublicKey, n)
	retAddr := make([]ledger.AddressED25519, n)
	util.Assertf(n > 0, "number of addresses must be positive")
	for i := 0; i < n; i++ {
		retPriv[i], retPub[i], retAddr[i] = u.GenerateAddress(startIndex + i)
	}
	return retPriv, retPub, retAddr
}

func (u *UTXODB) GenerateAddressesWithFaucetAmount(startIndex int, n int, amount uint64) ([]ed25519.PrivateKey, []ed25519.PublicKey, []ledger.AddressED25519) {
	retPriv, retPub, retAddr := u.GenerateAddresses(startIndex, n)
	err := u.TokensFromFaucetMulti(retAddr, amount)
	util.AssertNoError(err)
	return retPriv, retPub, retAddr
}

func (u *UTXODB) MakeTransferInputData(privKey ed25519.PrivateKey, sourceAccount ledger.Accountable, ts ledger.Time, desc ...bool) (*txbuilder.TransferData, error) {
	if ts == ledger.NilLedgerTime {
		ts = ledger.TimeNow()
	}
	ret := txbuilder.NewTransferData(privKey, sourceAccount, ts)

	switch addr := ret.SourceAccount.(type) {
	case ledger.AddressED25519:
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
	outsData, err := u.StateReader().GetUTXOsLockedInAccount(par.SourceAccount.AccountID())
	if err != nil {
		return err
	}
	outs, err := txutils.ParseAndSortOutputData(outsData, func(o *ledger.Output) bool {
		return o.Lock().UnlockableWith(par.SourceAccount.AccountID(), par.Timestamp)
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
	par, err := u.MakeTransferInputData(privKey, nil, ledger.NilLedgerTime)
	if err != nil {
		return nil, err
	}
	par.WithAmount(amount).
		WithTargetLock(targetLock)
	txBytes, err := txbuilder.MakeTransferTransaction(par)
	if err != nil {
		return nil, err
	}
	return txBytes, u.AddTransaction(txBytes, func(ctx *transaction.TxContext, err error) error {
		if err != nil {
			return fmt.Errorf("Error: %v\n%s", err, ctx.String())
		}
		return nil
	})
}

func (u *UTXODB) TransferTokens(privKey ed25519.PrivateKey, targetLock ledger.Lock, amount uint64) error {
	_, err := u.transferTokens(privKey, targetLock, amount)
	return err
}

func (u *UTXODB) account(addr ledger.Accountable, ts ...ledger.Time) (uint64, int) {
	outs, err := u.StateReader().GetUTXOsLockedInAccount(addr.AccountID())
	util.AssertNoError(err)
	balance := uint64(0)
	var filter func(o *ledger.Output) bool
	if len(ts) > 0 {
		filter = func(o *ledger.Output) bool {
			return o.Lock().UnlockableWith(addr.AccountID(), ts[0])
		}
	}
	outs1, err := txutils.ParseAndSortOutputData(outs, filter)
	util.AssertNoError(err)

	for _, o := range outs1 {
		balance += o.Output.Amount()
	}
	return balance, len(outs1)
}

// Balance returns balance of address unlockable at timestamp ts, if provided. Otherwise, all outputs taken
// For chains, this does not include te chain-output itself
func (u *UTXODB) Balance(addr ledger.Accountable, ts ...ledger.Time) uint64 {
	ret, _ := u.account(addr, ts...)
	return ret
}

// BalanceOnChain returns balance locked in chain and separately balance on chain output
func (u *UTXODB) BalanceOnChain(chainID ledger.ChainID) (uint64, uint64, error) {
	outChain, outs, err := txbuilder.GetChainAccount(chainID, u.StateReader())
	if err != nil {
		return 0, 0, err
	}
	amount := uint64(0)
	for _, odata := range outs {
		amount += odata.Output.Amount()
	}
	return amount, outChain.Output.Amount(), nil
}

// NumUTXOs returns number of outputs of address unlockable at timestamp ts, if provided. Otherwise, all outputs taken
func (u *UTXODB) NumUTXOs(addr ledger.Accountable, ts ...ledger.Time) int {
	_, ret := u.account(addr, ts...)
	return ret
}

func (u *UTXODB) DoTransferTx(par *txbuilder.TransferData) ([]byte, error) {
	txBytes, err := txbuilder.MakeTransferTransaction(par)
	if err != nil {
		return nil, err
	}
	return txBytes, u.AddTransaction(txBytes, func(ctx *transaction.TxContext, err error) error {
		if err != nil {
			return fmt.Errorf("Error: %v\n%s", err, ctx.String())
		}
		return nil
	})
}

func (u *UTXODB) DoTransferOutputs(par *txbuilder.TransferData) ([]*ledger.OutputWithID, error) {
	txBytes, err := txbuilder.MakeSimpleTransferTransaction(par)
	if err != nil {
		return nil, err
	}
	if err = u.AddTransaction(txBytes, func(ctx *transaction.TxContext, err error) error {
		if err != nil {
			return fmt.Errorf("Error: %v\n%s", err, ctx.String())
		}
		return nil
	}); err != nil {
		return nil, err
	}
	tx, err := transaction.FromBytes(txBytes)
	if err != nil {
		return nil, err
	}
	return tx.ProducedOutputs(), nil
}

func (u *UTXODB) DoTransfer(par *txbuilder.TransferData) error {
	_, err := u.DoTransferTx(par)
	return err
}

func (u *UTXODB) ValidationContextFromTransaction(txBytes []byte) (*transaction.TxContext, error) {
	return transaction.TxContextFromTransferableBytes(txBytes, u.state.Readable().GetUTXO)
}

func (u *UTXODB) TxToString(txbytes []byte) string {
	ctx, err := u.ValidationContextFromTransaction(txbytes)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return ctx.String()
}

// CreateChainOrigin takes all tokens from controller address and puts them on the chain output
func (u *UTXODB) CreateChainOrigin(controllerPrivateKey ed25519.PrivateKey, ts ledger.Time) (ledger.ChainID, error) {
	controllerAddress := ledger.AddressED25519FromPrivateKey(controllerPrivateKey)
	amount := u.Balance(controllerAddress)
	td, err := u.MakeTransferInputData(controllerPrivateKey, controllerAddress, ts)
	if err != nil {
		return [32]byte{}, err
	}
	outs, err := u.DoTransferOutputs(td.
		WithAmount(amount).
		WithTargetLock(controllerAddress).
		WithConstraint(ledger.NewChainOrigin()),
	)
	if err != nil {
		return [32]byte{}, err
	}
	chains, err := txutils.FilterChainOutputs(outs)
	if err != nil {
		return [32]byte{}, err
	}
	return chains[0].ChainID, nil

}

func (u *UTXODB) OriginDistributionTransactionString() string {
	genesisStemOutputID := ledger.GenesisStemOutputID()
	genesisOutputID := ledger.GenesisOutputID()

	return transaction.ParseBytesToString(u.originDistributionTxBytes, func(oid *ledger.OutputID) ([]byte, bool) {
		switch *oid {
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
