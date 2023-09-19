package api

const (
	PathGetAccountOutputs = "/get_account_outputs"
	PathGetChainOutput    = "/get_chain_output"
	PathGetOutput         = "/get_output"
	PathSubmitTransaction = "/submit_tx"
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
	OutputData string `json:"output_data,omitempty"`
}
