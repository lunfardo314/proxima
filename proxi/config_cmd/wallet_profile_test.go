package config_cmd

import (
	"bytes"
	"testing"
	"text/template"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// The generated wallet profile must be valid YAML that reads back as proxi
// reads it: one primary node URL and the witness list of kb/api_witnesses.md
// holding every public node.
func TestWalletProfileTemplateRenders(t *testing.T) {
	templ, err := template.New("wallet").Parse(walletProfileTemplate)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, templ.Execute(&buf, struct {
		KeyFile, HolderID, BootstrapSeqID string
		PublicNodes, AllPublicNodes       []publicNode
	}{
		KeyFile: "k.key", HolderID: "00", BootstrapSeqID: "00",
		PublicNodes: walletHintNodes(), AllPublicNodes: publicNodes,
	}))

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(&buf))
	require.Equal(t, "http://127.0.0.1:8000", v.GetString("api.node_url"))
	urls := v.GetStringSlice("api.node_urls")
	require.Len(t, urls, len(publicNodes))
	for i, n := range publicNodes {
		require.Equal(t, n.APIEndpoint(), urls[i])
	}
}
