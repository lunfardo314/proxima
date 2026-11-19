package glb

import (
	"os"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// withStdin replaces os.Stdin by a pipe carrying input for the duration of the test.
func withStdin(t *testing.T, input string) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(input)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	saved := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = saved; _ = r.Close() })
}

// A command run with -f answers every confirmation with its default, also where the caller passes no
// force argument: there the prompt reads the flag itself. Without -f the answer is read from the console.
func TestYesNoPromptForce(t *testing.T) {
	saved := viper.GetBool("force")
	t.Cleanup(func() { viper.Set("force", saved) })

	viper.Set("force", true)
	withStdin(t, "n\n") // would answer no, but must not be read
	require.True(t, YesNoPrompt("proceed?", true))
	require.False(t, YesNoPrompt("proceed?", false))

	viper.Set("force", false)
	withStdin(t, "n\n")
	require.False(t, YesNoPrompt("proceed?", true), "without -f the console answer counts")
	withStdin(t, "y\n")
	require.True(t, YesNoPrompt("proceed?", true, false), "an explicit false asks too")
}
