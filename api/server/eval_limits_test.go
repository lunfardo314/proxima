package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// /api/v1/eval evaluates under the library's evaluation rules (call depth, call count and, from library
// upgrade 7, strict encoding). selectCaseByIndex(0xff, 0x01) is empty under the strict rule and 0xff
// without any rules, so the result tells whether the rules applied.
func TestEvalAppliesEngineRules(t *testing.T) {
	require.True(t, ledger.L(base.MaxSlot).HasUpgrade7(), "the test genesis folds upgrade 7, the strict rule applies")

	body, err := json.Marshal(api.EvalRequest{Sources: []string{"selectCaseByIndex(0xff, 0x01)"}})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, api.PathEval, bytes.NewReader(body))
	w := httptest.NewRecorder()
	(&server{}).eval(w, req)

	data, err := io.ReadAll(w.Result().Body)
	require.NoError(t, err)
	var out api.EvalResponse
	require.NoError(t, json.Unmarshal(data, &out))
	require.Len(t, out.Results, 1)
	require.Empty(t, out.Results[0].Error)
	require.Empty(t, out.Results[0].Value, "evaluated without the library's rules (result %q instead of empty)", out.Results[0].Value)
}

// hashTerm builds about 975 KiB of hash input from about 440 characters of source.
func hashTerm() string {
	x := "repeat(repeat(0xaa,255),255)"
	parts := make([]string, 15)
	for i := range parts {
		parts[i] = x
	}
	return "blake2b(" + strings.Join(parts, ",") + ")"
}

// costlySource is 15 hash terms (concat takes at most 15 arguments), within the per-source limits.
func costlySource() string {
	terms := make([]string, 15)
	for i := range terms {
		terms[i] = hashTerm()
	}
	return "concat(" + strings.Join(terms, ",") + ")"
}

// Each source of an /api/v1/eval request is bounded, and so is the request body, and the sources of
// one request together stay within the per-source limit: the costliest request the handler accepts,
// and one filling the whole body, both finish within a second.
func TestEvalRequestCostBounded(t *testing.T) {
	src := costlySource()
	require.NoError(t, checkSourceShape(src), "every source is within the handler's limits")

	for _, n := range []int{(maxTxUploadSize - 1024) / (len(src) + 3), maxSourceSize / len(src)} {
		sources := make([]string, n)
		for i := range sources {
			sources[i] = src
		}
		body, err := json.Marshal(api.EvalRequest{Sources: sources})
		require.NoError(t, err)
		require.Less(t, len(body), maxTxUploadSize)

		srv := &server{}
		done := make(chan struct{})
		start := time.Now()
		go func() {
			req := httptest.NewRequest(http.MethodPost, api.PathEval, bytes.NewReader(body))
			srv.eval(httptest.NewRecorder(), req)
			close(done)
		}()
		select {
		case <-done:
			t.Logf("%d sources of %d bytes: request done after %v", n, len(src), time.Since(start))
		case <-time.After(time.Second):
			t.Fatalf("one /eval request (%d KiB, %d sources) still running after 1 s", len(body)/1024, n)
		}
	}
}
