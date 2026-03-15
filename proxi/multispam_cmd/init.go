package multispam_cmd

import (
	"fmt"
	"os"

	"github.com/lunfardo314/proxima/multispam"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "generate sender keys and create multispam.yaml (or add senders to existing)",
		Args:  cobra.NoArgs,
		Run:   runInitCmd,
	}
	cmd.Flags().IntP("senders", "n", 3, "number of sender keys to generate")
	cmd.Flags().String("api-host", "", "API host URL (default: from proxi.yaml api.endpoint)")
	cmd.Flags().String("keys-dir", "keys", "directory for key files")
	cmd.Flags().Bool("add", false, "add new senders to existing config instead of creating new")
	return cmd
}

func runInitCmd(cmd *cobra.Command, _ []string) {
	glb.TryReadInConfig()
	addMode, _ := cmd.Flags().GetBool("add")
	if addMode {
		runAddSenders(cmd)
	} else {
		runCreateNew(cmd)
	}
}

func runCreateNew(cmd *cobra.Command) {
	configFile := viper.GetString("multispam-config")
	if glb.FileExists(configFile) {
		glb.Fatalf("'%s' already exists. Use --add to add senders, or remove the file to re-initialize", configFile)
	}

	numSenders, _ := cmd.Flags().GetInt("senders")
	if numSenders < 2 {
		glb.Fatalf("need at least 2 senders, got %d", numSenders)
	}

	apiHost, _ := cmd.Flags().GetString("api-host")
	if apiHost == "" {
		apiHost = viper.GetString("api.endpoint")
	}
	if apiHost == "" {
		glb.Fatalf("API host not specified. Use --api-host or configure api.endpoint in proxi.yaml")
	}

	keysDir, _ := cmd.Flags().GetString("keys-dir")

	if err := os.MkdirAll(keysDir, 0700); err != nil {
		glb.Fatalf("can't create keys directory '%s': %v", keysDir, err)
	}

	cfg := multispam.GenerateDefaultConfig(numSenders, apiHost, keysDir)

	glb.Infof("generating %d sender keys in '%s/'...", numSenders, keysDir)
	generateKeys(cfg.Senders)

	if err := multispam.SaveConfig(cfg, configFile); err != nil {
		glb.Fatalf("can't save config: %v", err)
	}

	glb.Infof("created '%s' with %d senders", configFile, numSenders)
	fmt.Printf("\nNext steps:\n")
	fmt.Printf("  1. Review and edit %s\n", configFile)
	fmt.Printf("  2. Fund accounts: proxi multispam fund --amount <tokens>\n")
	fmt.Printf("  3. Check balances: proxi multispam info\n")
	fmt.Printf("  4. Run: proxi multispam run\n")
}

func runAddSenders(cmd *cobra.Command) {
	configFile := viper.GetString("multispam-config")
	if !glb.FileExists(configFile) {
		glb.Fatalf("'%s' not found. Run 'proxi multispam init' first (without --add)", configFile)
	}

	cfg, err := multispam.LoadConfig(configFile)
	glb.AssertNoError(err)

	numNew, _ := cmd.Flags().GetInt("senders")
	if numNew < 1 {
		glb.Fatalf("need at least 1 sender to add, got %d", numNew)
	}

	keysDir, _ := cmd.Flags().GetString("keys-dir")
	if err := os.MkdirAll(keysDir, 0700); err != nil {
		glb.Fatalf("can't create keys directory '%s': %v", keysDir, err)
	}

	// Determine starting index from existing senders
	startIdx := len(cfg.Senders) + 1

	newSenders := make([]multispam.SenderConfig, numNew)
	for i := range newSenders {
		newSenders[i] = multispam.SenderConfig{
			Name:    fmt.Sprintf("sender%d", startIdx+i),
			KeyFile: fmt.Sprintf("%s/sender%d.key", keysDir, startIdx+i),
		}
	}

	glb.Infof("adding %d sender keys in '%s/'...", numNew, keysDir)
	generateKeys(newSenders)

	cfg.Senders = append(cfg.Senders, newSenders...)

	if err := multispam.SaveConfig(cfg, configFile); err != nil {
		glb.Fatalf("can't save config: %v", err)
	}

	glb.Infof("updated '%s': now %d senders total", configFile, len(cfg.Senders))
}

func generateKeys(senders []multispam.SenderConfig) {
	for _, s := range senders {
		if glb.FileExists(s.KeyFile) {
			glb.Fatalf("key file '%s' already exists", s.KeyFile)
		}
		holderID, err := multispam.GenerateAndSaveKey(s.KeyFile)
		glb.AssertNoError(err)
		glb.Infof("  %s: %s (holder ID: %s)", s.Name, s.KeyFile, holderID)
	}
}
