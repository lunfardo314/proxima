package console

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	verboseFlag bool
	debugFlag   bool
)

func Init(rootCmd *cobra.Command) {
	rootCmd.PersistentFlags().BoolVarP(&verboseFlag, "verbose", "v", false, "verbose")
	rootCmd.PersistentFlags().BoolVarP(&debugFlag, "debug", "d", false, "verbose")
}

func Printf(format string, args ...any) {
	fmt.Printf(format, args...)
}

func Infof(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

func Debugf(format string, args ...any) {
	if debugFlag {
		fmt.Printf(format+"\n", args...)
	}
}

func Verbosef(format string, args ...any) {
	if verboseFlag {
		fmt.Printf(format+"\n", args...)
	}
}

func Fatalf(format string, args ...any) {
	fmt.Printf("Error: "+format+"\n", args...)
	os.Exit(1)
}

func NoError(err error) {
	if err != nil {
		Fatalf("error: %v", err)
	}
}

func Assertf(cond bool, format string, args ...any) {
	if !cond {
		Fatalf(format, args...)
	}
}

func YesNoPrompt(label string, def bool) bool {
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
