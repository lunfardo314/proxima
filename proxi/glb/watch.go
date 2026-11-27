package glb

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// WatchEntry is one named holder ID of the profile's watch list (kb/proxi_watch.md).
type WatchEntry struct {
	Name     string
	HolderID base.HolderID
}

const (
	WatchNameWallet = "wallet"
	WatchNameCold   = "cold"
)

// GetWatchList returns the implied 'wallet' and 'cold' entries, when configured, followed
// by the profile's 'watch' section in file order.
func GetWatchList() ([]WatchEntry, error) {
	ret := make([]WatchEntry, 0)
	if viper.GetString("wallet.key_file") != "" {
		var hid base.HolderID
		copy(hid[:], GetWalletAccount().ControllerID())
		ret = append(ret, WatchEntry{Name: WatchNameWallet, HolderID: hid})
	}
	cold, err := GetColdWallet()
	if err != nil {
		return nil, err
	}
	if cold != nil {
		ret = append(ret, WatchEntry{Name: WatchNameCold, HolderID: cold.HolderID})
	}
	data, err := os.ReadFile(viper.ConfigFileUsed())
	if err != nil {
		return nil, err
	}
	listed, err := parseWatchSection(data)
	if err != nil {
		return nil, err
	}
	ret = append(ret, listed...)

	names := make(map[string]bool)
	holders := make(map[base.HolderID]string)
	for _, e := range ret {
		if names[e.Name] {
			return nil, fmt.Errorf("watch: name '%s' is given twice ('%s' and '%s' are implied)", e.Name, WatchNameWallet, WatchNameCold)
		}
		names[e.Name] = true
		if other, dup := holders[e.HolderID]; dup {
			return nil, fmt.Errorf("watch: holder ID %s is listed under both '%s' and '%s'", hex.EncodeToString(e.HolderID[:]), other, e.Name)
		}
		holders[e.HolderID] = e.Name
	}
	return ret, nil
}

// parseWatchSection reads the 'watch' mapping from the raw profile. Not read through viper,
// which lowercases keys and silently keeps the last of two equal ones, so it could neither
// report a duplicate nor keep the order.
func parseWatchSection(profile []byte) ([]WatchEntry, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(profile, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil
	}
	top := doc.Content[0].Content
	var section *yaml.Node
	for i := 0; i+1 < len(top); i += 2 {
		if top[i].Value == "watch" {
			section = top[i+1]
		}
	}
	if section == nil || (section.Kind == yaml.ScalarNode && section.Value == "") {
		return nil, nil
	}
	if section.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("watch: expected a mapping of names to holder IDs")
	}
	ret := make([]WatchEntry, 0, len(section.Content)/2)
	for i := 0; i+1 < len(section.Content); i += 2 {
		name, value := section.Content[i].Value, strings.TrimSpace(section.Content[i+1].Value)
		if name == WatchNameWallet || name == WatchNameCold {
			return nil, fmt.Errorf("watch: name '%s' is reserved for the implied entry", name)
		}
		b, err := hex.DecodeString(strings.TrimPrefix(value, "a/"))
		if err != nil || len(b) != len(base.HolderID{}) {
			return nil, fmt.Errorf("watch: '%s: %s' is not a %d-byte hex holder ID", name, value, len(base.HolderID{}))
		}
		e := WatchEntry{Name: name}
		copy(e.HolderID[:], b)
		ret = append(ret, e)
	}
	return ret, nil
}
