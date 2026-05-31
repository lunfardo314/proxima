package config_cmd

import (
	"bytes"
	"crypto/ed25519"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/template"
	"time"

	p2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

//go:embed node_config.template
var configFileTemplateRaw string

// configFileTemplate has CRLF normalized to LF so it renders identically regardless
// of the line endings the embedded file happens to have on disk (git autocrlf on
// Windows/WSL checkouts can introduce CR bytes).
var configFileTemplate = strings.ReplaceAll(configFileTemplateRaw, "\r\n", "\n")

var (
	includeSeq        bool
	includeStandalone bool
	includeTrace      bool
	seqName           string
)

func configNodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Args:  cobra.NoArgs,
		Short: "creates config file for the Proxima node, or adds a sequencer section to an existing one",
		Run:   runConfigNodeCommand,
	}

	cmd.PersistentFlags().BoolVar(&includeSeq, "sequencer", false, "include sequencer config section (disabled, placeholder chain ID). If proxima.yaml exists, only the sequencer section is added")
	err := viper.BindPFlag("sequencer", cmd.PersistentFlags().Lookup("sequencer"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().BoolVar(&includeStandalone, "standalone", false, "include enabled bootstrap sequencer config with standalone=true (single-node dev network)")
	err = viper.BindPFlag("standalone", cmd.PersistentFlags().Lookup("standalone"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().BoolVar(&includeTrace, "trace", false, "include trace_tags and txlogger config sections (disabled)")
	err = viper.BindPFlag("trace", cmd.PersistentFlags().Lookup("trace"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().StringVar(&seqName, "name", "", "sequencer name (1-6 chars), used with --sequencer/--standalone")
	err = viper.BindPFlag("name", cmd.PersistentFlags().Lookup("name"))
	glb.AssertNoError(err)

	return cmd
}

const (
	proximaNodeProfile     = "proxima.yaml"
	peeringPort            = 4000
	apiPort                = 8000
	defaultMaxDynamicPeers = 10
)

type configFileData struct {
	HostPrivateKey string
	HostID         string
	HostPort       int
	APIPort        int
	StaticPeers    []struct {
		Name      string
		MultiAddr string
	}
	MaxDynamicPeers   int
	IncludeTrace      bool
	IncludeTxLogger   bool
	TxLoggerEnabled   bool
	IncludeSequencer  bool
	SeqName           string
	HasSeqName        bool
	SeqEnable         string
	SeqChainID        string
	ControllerKeyFile string
	Standalone        bool
}

func runConfigNodeCommand(_ *cobra.Command, _ []string) {
	templ := template.New("config")
	_, err := templ.Parse(configFileTemplate)
	glb.AssertNoError(err)

	includeSequencer := includeSeq || includeStandalone

	// Edit mode: proxima.yaml exists and the user wants to add/replace just the sequencer section.
	// --standalone is meant for fresh setup only.
	if glb.FileExists(proximaNodeProfile) && includeSeq && !includeStandalone {
		updateSequencerSection(templ)
		return
	}

	glb.Assertf(!glb.FileExists(proximaNodeProfile), "file %s already exists", proximaNodeProfile)

	// In --standalone we need the wallet key to build the genesis snapshot.
	// Load it (and prompt for passphrase if needed) before asking for host
	// entropy so we fail early on a misconfigured wallet.
	var walletKey ed25519.PrivateKey
	if includeStandalone {
		glb.TryReadInConfig()
		walletKey = glb.MustGetPrivateKey()
	}

	privateKey := glb.AskEntropyGenEd25519PrivateKey("please enter at least 10 random seed symbols for the private key and ID of the peering host and press ENTER:", 10)
	pklpp, err := p2pcrypto.UnmarshalEd25519PrivateKey(privateKey)
	util.AssertNoError(err)
	hid, err := peer.IDFromPrivateKey(pklpp)
	util.AssertNoError(err)

	data := configFileData{
		HostPrivateKey:    hex.EncodeToString(privateKey),
		HostID:            hid.String(),
		HostPort:          peeringPort,
		APIPort:           apiPort,
		StaticPeers:       nil,
		MaxDynamicPeers:   defaultMaxDynamicPeers,
		IncludeTrace:      includeTrace,
		IncludeTxLogger:   includeTrace,
		TxLoggerEnabled:   false,
		IncludeSequencer:  includeSequencer,
		SeqName:           seqName,
		HasSeqName:        seqName != "",
		SeqEnable:         "false",
		SeqChainID:        "<sequencer id hex encoded>",
		ControllerKeyFile: resolveControllerKeyFile(),
	}
	if includeStandalone {
		if seqName == "" {
			data.SeqName = "boot"
		}
		data.HasSeqName = true
		data.SeqEnable = "true"
		data.SeqChainID = ledger.BoostrapSequencerIDHex
		data.Standalone = true
		data.IncludeTxLogger = true
		data.TxLoggerEnabled = true
	}

	var buf bytes.Buffer
	err = templ.Execute(&buf, data)
	glb.AssertNoError(err)

	err = os.WriteFile(proximaNodeProfile, buf.Bytes(), 0600)
	glb.AssertNoError(err)

	glb.Infof("initial Proxima node configuration file has been saved as '%s'", proximaNodeProfile)

	if includeStandalone {
		createStandaloneGenesisSnapshot(walletKey)
	}
}

// createStandaloneGenesisSnapshot writes a genesis snapshot for a single-node
// developer ledger into the current directory. Called from the --standalone path.
func createStandaloneGenesisSnapshot(privateKey ed25519.PrivateKey) {
	genesisTimeUnix := uint32(time.Now().Unix())
	description := fmt.Sprintf("Proxima standalone developer ledger %s",
		time.Unix(int64(genesisTimeUnix), 0).UTC().Format("2006.01.02 15:04:05"))

	glb.Infof("Creating genesis snapshot for standalone developer ledger...")
	glb.Infof("  Description: '%s'", description)

	data, err := multistate.BuildGenesisSnapshotData(privateKey, genesisTimeUnix, description)
	glb.AssertNoError(err)

	fpath, err := multistate.WriteGenesisSnapshot(data, ".", os.Stdout)
	glb.AssertNoError(err)

	glb.Infof("Genesis snapshot created: %s", fpath)
}

// updateSequencerSection adds or replaces the `sequencer:` section in an existing
// proxima.yaml. The rest of the file is untouched.
func updateSequencerSection(templ *template.Template) {
	existing, err := os.ReadFile(proximaNodeProfile)
	glb.AssertNoError(err)

	data := configFileData{
		SeqName:           seqName,
		HasSeqName:        seqName != "",
		SeqEnable:         "false",
		SeqChainID:        "<sequencer id hex encoded>",
		ControllerKeyFile: resolveControllerKeyFile(),
	}
	var blockBuf bytes.Buffer
	err = templ.ExecuteTemplate(&blockBuf, "sequencer_block", data)
	glb.AssertNoError(err)
	block := blockBuf.Bytes()

	start, end, found := findSequencerSection(existing)
	if found {
		if !glb.YesNoPrompt("sequencer section already exists in '"+proximaNodeProfile+"'. Overwrite?", false) {
			glb.Infof("aborted; '%s' left untouched", proximaNodeProfile)
			return
		}
		updated := append(append([]byte{}, existing[:start]...), block...)
		updated = append(updated, existing[end:]...)
		err = os.WriteFile(proximaNodeProfile, updated, 0600)
		glb.AssertNoError(err)
		glb.Infof("sequencer section in '%s' has been replaced", proximaNodeProfile)
		return
	}

	// Append the section, ensuring a single blank line separator.
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		existing = append(existing, '\n')
	}
	existing = append(existing, '\n')
	existing = append(existing, block...)
	err = os.WriteFile(proximaNodeProfile, existing, 0600)
	glb.AssertNoError(err)
	glb.Infof("sequencer section has been appended to '%s'", proximaNodeProfile)
}

// sequencerHeaderRe matches a top-level `sequencer:` key (start of line, no indent).
var sequencerHeaderRe = regexp.MustCompile(`(?m)^sequencer:[ \t]*(#.*)?\r?$`)

// findSequencerSection locates a top-level `sequencer:` block in YAML bytes.
// Returns [start, end) byte offsets to replace. The block runs from the start of
// the `sequencer:` line up to (but not including) the next top-level key line.
// If the section is preceded by a `# Sequencer configuration` comment header,
// that header line is included so it gets replaced too.
func findSequencerSection(yamlBytes []byte) (start, end int, found bool) {
	loc := sequencerHeaderRe.FindIndex(yamlBytes)
	if loc == nil {
		return 0, 0, false
	}
	start = loc[0]

	// Scan forward from the line after the header to find the end of the block:
	// the next non-blank, non-indented, non-comment line that introduces a new
	// top-level key (or EOF).
	i := loc[1]
	if i < len(yamlBytes) && yamlBytes[i] == '\n' {
		i++
	}
	end = len(yamlBytes)
	for i < len(yamlBytes) {
		lineStart := i
		for i < len(yamlBytes) && yamlBytes[i] != '\n' {
			i++
		}
		line := yamlBytes[lineStart:i]
		if i < len(yamlBytes) {
			i++ // consume '\n'
		}
		if len(line) == 0 {
			continue
		}
		first := line[0]
		if first == ' ' || first == '\t' || first == '#' {
			continue
		}
		end = lineStart
		break
	}

	// Pull the preceding "# Sequencer configuration" header into the replaced range
	// if present, to avoid leaving a stale comment.
	if start > 0 {
		// Walk backwards to the previous '\n' to find the start of the preceding line.
		prevEnd := start - 1 // index of the '\n' that terminates the previous line
		if prevEnd >= 0 && yamlBytes[prevEnd] == '\n' {
			prevLineStart := prevEnd
			for prevLineStart > 0 && yamlBytes[prevLineStart-1] != '\n' {
				prevLineStart--
			}
			prev := yamlBytes[prevLineStart:prevEnd]
			if bytes.HasPrefix(prev, []byte("# Sequencer configuration")) {
				start = prevLineStart
			}
		}
	}
	return start, end, true
}

// resolveControllerKeyFile returns the keystore path the sequencer should use as
// controller. Prefers `wallet.key_file` from proxi.yaml if available, falling back
// to the default keystore filename.
func resolveControllerKeyFile() string {
	glb.TryReadInConfig()
	if k := viper.GetString("wallet.key_file"); k != "" {
		return k
	}
	return keystore.DefaultKeyFile
}
