package glb

// Witness nodes confirm the one value the library commitment proof cannot
// derive: the branch ID the node named. A branch that other nodes have
// committed, and that sits on their reliable lineage once they are past its
// slot, is one the honest network produced. See kb/api_witnesses.md.

import (
	"fmt"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/spf13/viper"
)

const witnessTimeout = 5 * time.Second

// WitnessURLs is the profile's 'api.node_urls' without the primary node and
// without repeats. The witnesses are asked about branch IDs and nothing else.
func WitnessURLs() []string {
	normalize := func(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }
	seen := map[string]bool{normalize(NodeAPIURL()): true}
	ret := make([]string, 0)
	for _, u := range viper.GetStringSlice("api.node_urls") {
		u = normalize(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		ret = append(ret, u)
	}
	return ret
}

// branchWitness is the part of the node API a witness answers.
type branchWitness interface {
	GetLatestReliableBranch() (*multistate.BranchDataJSONAble, base.TransactionID, error)
	GetBranchChainTo(toBranch base.TransactionID, fromSlot uint32) ([]base.TransactionID, uint32, error)
}

// VerifyBranchWithWitnesses asks every configured witness whether branchID is
// a branch it has committed and, where the witness is already past that slot,
// whether the branch is on its reliable lineage. Every reachable witness must
// agree and at least one must be reachable. No witnesses configured is a
// warning, not a failure: a standalone or private network has nobody to ask.
func VerifyBranchWithWitnesses(branchID base.TransactionID) error {
	urls := WitnessURLs()
	if len(urls) == 0 {
		Infof("WARNING: no witness nodes in the profile ('api.node_urls'): the branch %s the library proof is anchored to is not cross-checked", branchID.StringShort())
		return nil
	}
	return verifyBranchWithWitnesses(branchID, urls, func(url string) branchWitness {
		return client.NewWithGoogleDNS(url, witnessTimeout)
	})
}

type witnessVerdict struct {
	url         string
	unreachable bool
	err         error
}

func verifyBranchWithWitnesses(branchID base.TransactionID, urls []string, connect func(url string) branchWitness) error {
	verdicts := make(chan witnessVerdict, len(urls))
	for _, url := range urls {
		go func(url string) {
			err, unreachable := checkWitness(connect(url), branchID)
			verdicts <- witnessVerdict{url: url, unreachable: unreachable, err: err}
		}(url)
	}
	reachable := 0
	var failures []string
	for range urls {
		v := <-verdicts
		switch {
		case v.unreachable:
			Infof("WARNING: witness %s is unreachable: %v", v.url, v.err)
		case v.err != nil:
			failures = append(failures, fmt.Sprintf("%s: %v", v.url, v.err))
		default:
			reachable++
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("branch %s named by the node %s is not confirmed by the witnesses:\n   %s",
			branchID.StringShort(), NodeAPIURL(), strings.Join(failures, "\n   "))
	}
	if reachable == 0 {
		return fmt.Errorf("no witness node is reachable, the branch %s named by the node %s cannot be confirmed", branchID.StringShort(), NodeAPIURL())
	}
	return nil
}

// checkWitness returns the witness's verdict about the branch. A witness that
// does not answer at all is unreachable; one that answers decides.
func checkWitness(w branchWitness, branchID base.TransactionID) (err error, unreachable bool) {
	_, witnessLRB, err := w.GetLatestReliableBranch()
	if err != nil {
		return err, true
	}
	// committed by the witness: the branch list handler answers from the committed
	// branches and refuses one it does not know
	if _, _, err = w.GetBranchChainTo(branchID, branchID.Slot()); err != nil {
		return fmt.Errorf("has not committed the branch: %v", err), false
	}
	// a witness past the branch's slot also knows whether the branch is on its reliable
	// lineage or on a fork that lost. One at the same slot cannot tell yet: two branches
	// of one slot are both candidates until the next slot settles it.
	if witnessLRB.Slot() <= branchID.Slot() {
		return nil, false
	}
	chain, _, err := w.GetBranchChainTo(witnessLRB, branchID.Slot())
	if err != nil {
		return err, true
	}
	for _, id := range chain {
		if id == branchID {
			return nil, false
		}
	}
	return fmt.Errorf("the branch is not on the lineage of the witness's reliable branch %s", witnessLRB.StringShort()), false
}
