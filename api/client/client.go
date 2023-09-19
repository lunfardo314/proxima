package client

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/core"
)

const apiDefaultClientTimeout = 3 * time.Second

type APIClient struct {
	c      http.Client
	prefix string
}

func New(serverURL string, timeout ...time.Duration) *APIClient {
	var to time.Duration
	if len(timeout) > 0 {
		to = timeout[0]
	} else {
		to = apiDefaultClientTimeout
	}
	return &APIClient{
		c:      http.Client{Timeout: to},
		prefix: serverURL,
	}
}

func (c *APIClient) GetAccountOutputs(accountable core.Accountable) ([]*core.OutputDataWithID, error) {
	path := fmt.Sprintf(api.PathGetAccountOutputs+"?accountable=%s", accountable.String())
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}

	var res api.OutputList
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, err
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}

	ret := make([]*core.OutputDataWithID, 0, len(res.Outputs))

	for idStr, dataStr := range res.Outputs {
		id, err := core.OutputIDFromHexString(idStr)
		if err != nil {
			return nil, fmt.Errorf("wrong output ID data from server: %s", idStr)
		}
		oData, err := hex.DecodeString(dataStr)
		if err != nil {
			return nil, fmt.Errorf("wrong output data from server: %s", dataStr)
		}
		ret = append(ret, &core.OutputDataWithID{
			ID:         id,
			OutputData: oData,
		})
	}

	sort.Slice(ret, func(i, j int) bool {
		return bytes.Compare(ret[i].ID[:], ret[j].ID[:]) < 0
	})
	return ret, nil
}

func (c *APIClient) GetChainOutput(chainID core.ChainID) (*core.OutputDataWithID, error) {
	path := fmt.Sprintf(api.PathGetChainOutput+"?chainid=%s", chainID.StringHex())
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}

	var res api.ChainOutput
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, err
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}

	oid, err := core.OutputIDFromHexString(res.OutputID)
	if err != nil {
		return nil, fmt.Errorf("wrong output ID data from server: %s", res.OutputID)
	}
	oData, err := hex.DecodeString(res.OutputData)
	if err != nil {
		return nil, fmt.Errorf("wrong output data from server: %s", res.OutputData)
	}

	return &core.OutputDataWithID{
		ID:         oid,
		OutputData: oData,
	}, nil
}

func (c *APIClient) GetOutputData(oid *core.OutputID) ([]byte, error) {
	path := fmt.Sprintf(api.PathGetOutput+"?id=%s", oid.StringHex())
	body, err := c.getBody(path)
	if err != nil {
		return nil, err
	}

	var res api.OutputData
	err = json.Unmarshal(body, &res)
	if err != nil {
		return nil, err
	}
	if res.Error.Error != "" {
		return nil, fmt.Errorf("from server: %s", res.Error.Error)
	}

	oData, err := hex.DecodeString(res.OutputData)
	if err != nil {
		return nil, fmt.Errorf("wrong output data from server: %s", res.OutputData)
	}

	return oData, nil
}

func (c *APIClient) getBody(path string) ([]byte, error) {
	url := c.prefix + path
	resp, err := c.c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}
