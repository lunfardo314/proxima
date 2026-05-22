package txbuildercore

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/lunfardo314/easyfl/engine"
	"golang.org/x/crypto/blake2b"
)

// RedeemScriptName is the canonical symbol of the tx-level constraint
// that commits a LocalScriptBin so callRedeemer(hash, fnIdx, …) in
// the same tx can resolve it. Mirrors ledger.SymRedeemScript.
const RedeemScriptName = "redeemScript"

// CallRedeemerName is the dispatch builtin invoked from UTXO
// constraints to call into a published local-script. Mirrors
// ledger.SymCallRedeemer.
const CallRedeemerName = "callRedeemer"

const (
	redeemScriptTemplate       = RedeemScriptName + "(0x%s)"
	callRedeemerNoArgsTemplate = CallRedeemerName + "(0x%s, 0x%02x)"
	callRedeemerTemplate       = CallRedeemerName + "(0x%s, 0x%02x, %s)"
)

// NewRedeemScriptConstraint emits the tx-level constraint
//
//	redeemScript(0x<bin>)
//
// `bin` is a LocalScriptBin produced by
// l.CompileLocalScript(...) on the wallet side. Push the
// result via TxBuilder.PushTxConstraint — it must sit at the
// TxConstraints position to be honoured by the validator.
func (l *Library[any]) NewRedeemScriptConstraint(bin engine.LocalScriptBin) ([]byte, error) {
	src := fmt.Sprintf(redeemScriptTemplate, hex.EncodeToString(bin))
	return l.CompileExpression(src)
}

// LocalScriptHash returns the 32-byte identifier callRedeemer expects
// when referring to a bin published by a redeemScript constraint in
// the same tx (or in the resolver's cache).
func LocalScriptHash(bin engine.LocalScriptBin) [32]byte {
	return blake2b.Sum256(bin)
}

// NewCallRedeemerConstraint emits
//
//	callRedeemer(0x<scriptHash>, 0x<fnIdx>, <argsSrc[0]>, <argsSrc[1]>, …)
//
// for use as an output constraint that dispatches into a published
// local-script. argsSrc carries raw EasyFL literal fragments —
// callRedeemer is variadic, so the caller picks the encoding per
// arg. Typical literal forms:
//
//	"z64/123"     // trimmed uint64
//	"z32/456"     // trimmed uint32
//	"z16/7"       // trimmed uint16
//	"0xdeadbeef"  // raw hex
//
// The wallet does not need to know the callee's expected types
// beyond the literal it emits.
func (l *Library[any]) NewCallRedeemerConstraint(scriptHash [32]byte, fnIdx byte, argsSrc ...string) ([]byte, error) {
	var src string
	if len(argsSrc) == 0 {
		src = fmt.Sprintf(callRedeemerNoArgsTemplate, hex.EncodeToString(scriptHash[:]), fnIdx)
	} else {
		src = fmt.Sprintf(callRedeemerTemplate, hex.EncodeToString(scriptHash[:]), fnIdx, strings.Join(argsSrc, ", "))
	}
	return l.CompileExpression(src)
}
