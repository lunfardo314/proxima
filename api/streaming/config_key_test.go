package streaming

// ConfigKey resolves the DAG streaming config keys across the
// api.streaming -> api.dag_streaming rename. The legacy spelling must keep
// working for node configs written before the rename.

import (
	"bytes"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// readConfig loads YAML into the viper singleton the node config is read from.
func readConfig(t *testing.T, yaml string) {
	t.Helper()
	viper.Reset()
	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(bytes.NewBufferString(yaml)))
}

func TestConfigKey(t *testing.T) {
	t.Run("current spelling", func(t *testing.T) {
		readConfig(t, `
api:
  dag_streaming:
    enable: true
    connection_ttl_minutes: 120
`)
		require.Equal(t, "api.dag_streaming.enable", ConfigKey("enable"))
		require.True(t, viper.GetBool(ConfigKey("enable")))
		require.Equal(t, 120, viper.GetInt(ConfigKey("connection_ttl_minutes")))
	})

	t.Run("legacy synonym", func(t *testing.T) {
		readConfig(t, `
api:
  streaming:
    enable: true
    connection_ttl_minutes: 7
`)
		require.Equal(t, "api.streaming.enable", ConfigKey("enable"))
		require.True(t, viper.GetBool(ConfigKey("enable")))
		require.Equal(t, 7, viper.GetInt(ConfigKey("connection_ttl_minutes")))
	})

	t.Run("current wins over legacy", func(t *testing.T) {
		readConfig(t, `
api:
  dag_streaming:
    enable: false
  streaming:
    enable: true
`)
		require.False(t, viper.GetBool(ConfigKey("enable")),
			"an explicit current-spelling value must not be overridden by the legacy one")
	})

	t.Run("per-subkey fallback", func(t *testing.T) {
		// a config that adopted the new key for one setting but left another
		// under the legacy spelling still resolves both
		readConfig(t, `
api:
  dag_streaming:
    enable: true
  streaming:
    max_connections: 3
`)
		require.True(t, viper.GetBool(ConfigKey("enable")))
		require.Equal(t, 3, viper.GetInt(ConfigKey("max_connections")))
	})

	t.Run("neither set falls back to the current spelling", func(t *testing.T) {
		readConfig(t, "api:\n  port: 8000\n")
		require.Equal(t, "api.dag_streaming.enable", ConfigKey("enable"))
		// unset -> zero value, so Run() applies its defaults
		require.False(t, viper.GetBool(ConfigKey("enable")))
		require.Zero(t, viper.GetInt(ConfigKey("max_connections")))
	})
}
