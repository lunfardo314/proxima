package version

import (
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func CmdVersion() *cobra.Command {
	verCmd := &cobra.Command{
		Use:     "version",
		Aliases: []string{"ver"},
		Args:    cobra.NoArgs,
		Short:   "displays version info of proxi",
		Run:     runVersionCmd,
	}
	verCmd.InitDefaultHelpCmd()
	return verCmd
}

func runVersionCmd(_ *cobra.Command, _ []string) {
	glb.Infof("    Version:      %s", global.Version)
	glb.Infof("    Commit time:  %s (%s ago)", global.CommitTime, time.Since(global.CommitTimeParsed).String())
	glb.Infof("    Commit hash:  %s", global.CommitHash)
}
