package config_cmd

import (
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

func CmdConfig() *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Args:  cobra.NoArgs,
		Short: "wallet and node configuration subcommands",
		Run: func(cmd *cobra.Command, args []string) {
		},
	}
	configCmd.AddCommand(
		configWalletCmd(),
		configNodeCmd(),
	)
	configCmd.InitDefaultHelpCmd()
	return configCmd
}

// stubKeyRe splits the content of a commented line (after its leading '#' and
// spaces are removed) into a YAML key and its value.
var stubKeyRe = regexp.MustCompile(`^([A-Za-z0-9_.-]+):(.*)$`)

// isCommentedConfigStub reports whether a trimmed whole-line comment is an
// uncomment-able config stub (`# key:`, `# key: value`, `# - item`) rather than
// prose. A prose sentence can begin `word:`, so a mapping stub qualifies only
// when its value is empty, a `<placeholder>`, or a single whitespace-free token;
// sentence-like values (with internal spaces) are treated as prose.
func isCommentedConfigStub(trimmed string) bool {
	rest := strings.TrimLeft(trimmed[1:], " \t") // drop the leading '#'
	if rest == "-" || strings.HasPrefix(rest, "- ") {
		return true // block-sequence entry
	}
	m := stubKeyRe.FindStringSubmatch(rest)
	if m == nil {
		return false
	}
	val := strings.TrimSpace(m[2])
	if val == "" || (strings.HasPrefix(val, "<") && strings.HasSuffix(val, ">")) {
		return true
	}
	return !strings.ContainsAny(val, " \t")
}

// stripWholeLineComments removes prose whole-line comments from rendered config
// templates while keeping commented-out config stubs (`# key: value`, `# - item`)
// so optional features stay discoverable and uncomment-able. End-of-line comments
// (`key: value  # note`) are kept intact. Blank-line runs left behind are collapsed
// to a single blank line, and leading/trailing blanks are trimmed. This produces
// the terse config; the verbose (-v) flag keeps the fully-commented template as-is.
func stripWholeLineComments(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	prevBlank := true // start true to drop leading blank lines
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") && !isCommentedConfigStub(trimmed) {
			continue
		}
		if trimmed == "" {
			if prevBlank {
				continue
			}
			prevBlank = true
			out = append(out, "")
			continue
		}
		prevBlank = false
		out = append(out, line)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n") + "\n"
}
