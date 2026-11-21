package util

import (
	"fmt"
	"strings"
)

// motesPerPROX equals base.PROX; util cannot import the ledger, a test pins the two equal.
const motesPerPROX = 1_000_000

// Prox renders an amount in motes as PROX for people to read: `,` thousands
// separators, up to 6 decimals with trailing zeros trimmed, ` PROX` suffix,
// e.g. 12_345_678_900 -> "12,345.6789 PROX". Machine-readable output stays in motes.
func Prox(motes uint64) string {
	ret := Th(motes/motesPerPROX, ",")
	if frac := motes % motesPerPROX; frac != 0 {
		ret += "." + strings.TrimRight(fmt.Sprintf("%06d", frac), "0")
	}
	return ret + " PROX"
}

// ProxSigned is Prox for a signed amount, such as a difference.
func ProxSigned(motes int64) string {
	if motes < 0 {
		return "-" + Prox(uint64(-motes))
	}
	return Prox(uint64(motes))
}
