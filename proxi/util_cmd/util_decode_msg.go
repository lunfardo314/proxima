package util_cmd

import (
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initDecodeMsgCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decode_msg_bytes <hex encoded binary>",
		Args:  cobra.ExactArgs(1),
		Short: fmt.Sprintf("decodes secure message payload"),
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runDecodeMsgCmd,
	}
	return cmd
}

func runDecodeMsgCmd(_ *cobra.Command, args []string) {
	msgBytes, err := hex.DecodeString(args[0])
	glb.Assertf(err == nil, "wrong parameter: %v", err)
	smap, err := base.SmallPersistentMapFromBytes(easyfl.StripDataPrefix(msgBytes))
	glb.Assertf(err == nil, "parse error: %v", err)
	glb.Infof("%s", smap.Lines("     ").String())
}
