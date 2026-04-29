package txstore

import (
	"fmt"
	"net/http"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/dag_explorer"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/txstore"
	"github.com/spf13/cobra"
)

func initDagExplorerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dag_explorer [--port 8080]",
		Short: "starts interactive DAG explorer in browser, reading from txstore DB",
		Args:  cobra.NoArgs,
		Run:   runDagExplorerCmd,
	}
	cmd.Flags().IntP("port", "p", 8080, "HTTP server port")
	return cmd
}

func runDagExplorerCmd(cmd *cobra.Command, _ []string) {
	glb.InitLedgerFromDB()
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()

	port, _ := cmd.Flags().GetInt("port")

	store, ok := glb.TxBytesStore().(*txstore.SimpleTxBytesStore)
	glb.Assertf(ok, "txstore does not support prefix iteration")

	mux := http.NewServeMux()
	dag_explorer.Register(mux.HandleFunc, store)
	// landing redirect for convenience: hitting "/" sends the user to the explorer page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, api.PathDAGExplorer, http.StatusFound)
	})

	addr := fmt.Sprintf(":%d", port)
	glb.Infof("DAG explorer listening on http://localhost%s%s", addr, api.PathDAGExplorer)
	if err := http.ListenAndServe(addr, mux); err != nil {
		glb.Assertf(false, "HTTP server failed: %v", err)
	}
}
