package glb

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/viper"
)

func Printf(format string, args ...any) {
	fmt.Printf(format, args...)
}

func Infof(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

func Verbosef(format string, args ...any) {
	if viper.GetBool("verbose") {
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

func YesNoPrompt(label string, def bool) bool {
	if viper.GetBool("force") {
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
