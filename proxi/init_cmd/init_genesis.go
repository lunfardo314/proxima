package init_cmd

import (
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initGenesisCmd() *cobra.Command {
	genesisCmd := &cobra.Command{
		Use:   "genesis",
		Short: "creates a genesis snapshot file from the wallet's private key",
		Long: `Creates a genesis snapshot file that can be used to bootstrap a new Proxima network.

The genesis snapshot contains:
- Ledger identity (genesis time + description)
- Library definitions at slot 0
- Genesis state with 3 outputs:
  - Initial supply output (locked to genesis controller)
  - Genesis stem output
  - Upgrade commitment UTXO

The snapshot file can be used by any node to bootstrap the network by placing it
in the snapshot directory. When a node starts without a database, it will
automatically restore from the latest available snapshot.`,
		Args: cobra.NoArgs,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runGenesisCmd,
	}

	genesisCmd.PersistentFlags().StringP("output", "o", ".", "output directory for the genesis snapshot file")
	genesisCmd.PersistentFlags().StringP("description", "d", "", "ledger description text (asked interactively if not given)")

	_ = viper.BindPFlag("output", genesisCmd.PersistentFlags().Lookup("output"))
	_ = viper.BindPFlag("description", genesisCmd.PersistentFlags().Lookup("description"))

	return genesisCmd
}

func runGenesisCmd(_ *cobra.Command, _ []string) {
	privateKey := glb.MustGetPrivateKey()
	outputDir := viper.GetString("output")
	description := viper.GetString("description")
	// The description is baked into the ledger identity and the genesis library
	// for the life of the ledger, so it is asked for rather than silently defaulted.
	if description == "" && !glb.BypassYesNoPrompt() {
		description = glb.TextPrompt(fmt.Sprintf("Ledger description text, up to %d bytes (Enter for default)", ledger.MaxDescriptionLength), "")
	}
	glb.Assertf(len(description) <= ledger.MaxDescriptionLength, "ledger description is %d bytes, maximum is %d", len(description), ledger.MaxDescriptionLength)

	// Use current time as genesis time
	genesisTimeUnix := uint32(time.Now().Unix())

	glb.Infof("Creating genesis snapshot...")
	glb.Infof("  Unix time now: %d (%s)", genesisTimeUnix, time.Unix(int64(genesisTimeUnix), 0).UTC().Format(time.RFC3339))
	glb.Infof("  Output directory: %s", outputDir)
	if description != "" {
		glb.Infof("  Description: '%s'", description)
	}

	// Build genesis data first to show constants before confirmation
	data, err := multistate.BuildGenesisSnapshotData(privateKey, genesisTimeUnix, description)
	glb.AssertNoError(err)

	libraryHash, err := data.GetLibraryHash()
	glb.AssertNoError(err)

	glb.Infof("\nGenesis parameters:\n%s", ledger.L(0).ConstantsString())
	glb.Infof("Library hash: %s", hex.EncodeToString(libraryHash[:]))
	glb.Infof("Bootstrap sequencer ID: %s", data.BootstrapChainID.String())

	if !glb.YesNoPrompt("\nProceed with creating genesis snapshot?", true) {
		glb.Fatalf("Aborted: genesis snapshot not created")
	}

	// Write the snapshot
	fpath, err := multistate.WriteGenesisSnapshot(data, outputDir, os.Stdout)
	glb.AssertNoError(err)

	glb.Infof("\nGenesis snapshot created successfully: %s", fpath)
	glb.Infof("\nTo start a new network:")
	glb.Infof("  1. Copy the snapshot file to your node's snapshot directory")
	glb.Infof("  2. Start the node - it will restore from the snapshot automatically")
}
