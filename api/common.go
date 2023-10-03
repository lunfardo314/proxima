package api

import (
	"github.com/lunfardo314/proxima/core"
)

const (
	PathGetAccountOutputs       = "/get_account_outputs"
	PathGetChainOutput          = "/get_chain_output"
	PathGetOutput               = "/get_output"
	PathSubmitTransactionWait   = "/submit_wait"   // wait appending to the utangle
	PathSubmitTransactionNowait = "/submit_nowait" // async submitting
	PathGetOutputWithInclusion  = "/inclusion"     // async submitting
)

type Error struct {
	// empty string when no error
	Error string `json:"error,omitempty"`
}

// OutputList is returned by 'get_account_outputs'
type OutputList struct {
	Error
	// key is hex-encoded outputID bytes
	// value is hex-encoded raw output data
	Outputs map[string]string `json:"outputs,omitempty"`
}

// ChainOutput is returned by 'get_chain_output'
type ChainOutput struct {
	Error
	// hex-encoded outputID
	OutputID string `json:"output_id,omitempty"`
	// hex-encoded output data
	OutputData string `json:"output_data,omitempty"`
}

// OutputData is returned by 'get_output'
type OutputData struct {
	Error
	// hex-encoded output data
	OutputData string      `json:"output_data,omitempty"`
	Inclusion  []Inclusion `json:"inclusion,omitempty"`
}

type Inclusion struct {
	BranchID string `json:"branch_id"`
	Coverage uint64 `json:"coverage"`
	Included bool   `json:"included"`
}

type InclusionDecoded struct {
	BranchID core.TransactionID
	Coverage uint64
	Included bool
}

const ErrGetOutputNotFound = "output not found"

func (i *Inclusion) Decode() (InclusionDecoded, error) {
	txid, err := core.TransactionIDFromHexString(i.BranchID)
	if err != nil {
		return InclusionDecoded{}, err
	}
	return InclusionDecoded{
		BranchID: txid,
		Coverage: i.Coverage,
		Included: i.Included,
	}, nil
}
