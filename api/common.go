package api

import (
	"errors"

	"github.com/lunfardo314/proxima/core"
)

const (
	PathGetAccountOutputs       = "/get_account_outputs"
	PathGetChainOutput          = "/get_chain_output"
	PathGetOutput               = "/get_output"
	PathSubmitTransactionWait   = "/submit_wait"   // wait appending to the utangle
	PathSubmitTransactionNowait = "/submit_nowait" // async submitting
	PathGetOutputInclusion      = "/inclusion"     // async submitting
)

var ErrNotSynced = errors.New("nodes not synced")

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
	OutputData string                 `json:"output_data,omitempty"`
	Inclusion  []InclusionDataEncoded `json:"inclusion,omitempty"`
}

type InclusionDataEncoded struct {
	BranchID string `json:"branch_id"`
	Coverage uint64 `json:"coverage"`
	Included bool   `json:"included"`
}

type InclusionData struct {
	BranchID core.TransactionID
	Coverage uint64
	Included bool
}

const ErrGetOutputNotFound = "output not found"

func (i *InclusionDataEncoded) Decode() (InclusionData, error) {
	txid, err := core.TransactionIDFromHexString(i.BranchID)
	if err != nil {
		return InclusionData{}, err
	}
	return InclusionData{
		BranchID: txid,
		Coverage: i.Coverage,
		Included: i.Included,
	}, nil
}
