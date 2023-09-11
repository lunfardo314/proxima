package log

import (
	"fmt"
	"os"

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
		fmt.Printf(format, args...)
	}
}

func Verbosef(format string, args ...any) {
	if verboseFlag {
		fmt.Printf(format, args...)
	}
}

func Fatalf(format string, args ...any) {
	fmt.Printf(format, args...)
	os.Exit(1)
}
