package glb

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

const (
	hid1 = "1111111111111111111111111111111111111111111111111111111111111111"
	hid2 = "2222222222222222222222222222222222222222222222222222222222222222"
)

// TestParseWatchSection checks that the watch section keeps file order and case, accepts
// the a/ prefix, and rejects every malformed form before the node is asked anything.
func TestParseWatchSection(t *testing.T) {
	t.Run("order and prefix", func(t *testing.T) {
		ret, err := parseWatchSection([]byte("api:\n  node_url: x\nwatch:\n  Zeta: " + hid1 + "\n  alpha: a/" + hid2 + "\n"))
		require.NoError(t, err)
		require.Len(t, ret, 2)
		require.Equal(t, "Zeta", ret[0].Name)
		require.Equal(t, hid1, ret[0].HolderID.String())
		require.Equal(t, "alpha", ret[1].Name)
		require.Equal(t, hid2, ret[1].HolderID.String())
	})
	t.Run("missing or empty section", func(t *testing.T) {
		for _, profile := range []string{"api:\n  node_url: x\n", "watch:\n", ""} {
			ret, err := parseWatchSection([]byte(profile))
			require.NoError(t, err)
			require.Empty(t, ret)
		}
	})
	t.Run("errors", func(t *testing.T) {
		for _, profile := range []string{
			"watch:\n  a: " + hid1[:62] + "\n",  // too short
			"watch:\n  a: zz" + hid1[2:] + "\n", // not hex
			"watch:\n  - " + hid1 + "\n",        // a list, not a mapping
			"watch:\n  wallet: " + hid1 + "\n",  // reserved name
			"watch:\n  cold: " + hid1 + "\n",    // reserved name
		} {
			_, err := parseWatchSection([]byte(profile))
			require.Error(t, err, profile)
		}
	})
	t.Run("duplicates are kept for the caller to reject", func(t *testing.T) {
		// yaml.v3 parses a repeated key into the node tree without complaint, so the
		// duplicate check in GetWatchList is the only one
		ret, err := parseWatchSection([]byte("watch:\n  a: " + hid1 + "\n  a: " + hid2 + "\n"))
		require.NoError(t, err)
		require.Len(t, ret, 2)
	})
}

// TestWatchListDuplicates checks the duplicate rules of GetWatchList on a profile file with
// no wallet key and no cold wallet, so only the 'watch' section is involved.
func TestWatchListDuplicates(t *testing.T) {
	run := func(section string) error {
		dir := t.TempDir()
		path := dir + "/proxi.yaml"
		require.NoError(t, os.WriteFile(path, []byte("watch:\n"+section), 0o600))
		viper.Reset()
		t.Cleanup(viper.Reset)
		viper.SetConfigFile(path)
		// error ignored as in ReadInConfig: viper refuses a profile with a repeated key as
		// a whole, and the watch list must still report the duplicate itself
		_ = viper.ReadInConfig()
		_, err := GetWatchList()
		return err
	}
	require.NoError(t, run("  a: "+hid1+"\n  b: "+hid2+"\n"))
	err := run("  a: " + hid1 + "\n  a: " + hid2 + "\n")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "given twice"), err.Error())
	err = run("  a: " + hid1 + "\n  b: " + hid1 + "\n")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "listed under both"), err.Error())
}
