package glb

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/spf13/viper"
)

func Infof(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

func IsVerbose() bool {
	return viper.GetBool("verbose") || viper.GetBool("v2")
}

func VerbosityLevel() int {
	if !IsVerbose() {
		return 0
	}
	if viper.GetBool("v2") {
		return 2
	}
	return 1
}

func Verbosef(format string, args ...any) {
	if IsVerbose() {
		fmt.Printf(format+"\n", args...)
	}
}

func Fatalf(format string, args ...any) {
	fmt.Printf("Error: "+format+"\n", args...)
	os.Exit(1)
}

func AssertNoError(err error) {
	if err != nil {
		Fatalf("error: %v", err)
	}
}

func Assertf(cond bool, format string, args ...any) {
	if !cond {
		Fatalf(format, args...)
	}
}

func YesNoPrompt(label string, def bool, force ...bool) bool {
	if len(force) > 0 && force[0] {
		return def
	}
	choices := "Y/n"
	if !def {
		choices = "y/N"
	}

	r := bufio.NewReader(os.Stdin)
	var s string

	for {
		fmt.Printf("%s (%s) ", label, choices)
		s, _ = r.ReadString('\n')
		s = strings.TrimSpace(s)
		if s == "" {
			return def
		}
		s = strings.ToLower(s)
		if s == "y" || s == "yes" {
			return true
		}
		if s == "n" || s == "no" {
			return false
		}
	}
}

func PrintLRB(lrbid *ledger.TransactionID) {
	Infof("Latest reliable branch (LRB) ID: %s, %d slot(s) from now", lrbid.String(), ledger.TimeNow().Slot()-lrbid.Slot())
}
