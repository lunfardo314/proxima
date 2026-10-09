package mine

import (
	"bytes"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/vrf"
)

// External nonce seekers (kb/external_nonce_seeker.md).
//
// With mine.seeker.listen set in the profile the miner serves its current
// search target as a job over HTTP and accepts nonces back. A seeker is a
// pure searcher: it holds the key, computes VRF outputs and returns a nonce.
// Everything that touches the chain stays here, and so does the proof: a
// returned nonce is recomputed under the wallet key, checked against the job
// and only then completed into a proof, so a wrong or hostile seeker can at
// most waste a verification. The seeker dials the miner, because a seeker on
// a rented machine can dial out but rarely be reached, and either side can
// restart without the other noticing more than a pause.

const (
	// longest a job long-poll is held before the current job is returned anyway
	seekerMaxWait = 10 * time.Second
	// how often the round waiter re-checks the local stop conditions while
	// waiting for a seeker's solution
	seekerWaitTick = 50 * time.Millisecond
)

// seekerJob is the wire form of the current search target. ID 0 means no job;
// the other fields are then omitted.
type seekerJob struct {
	ID          uint64 `json:"id"`
	AlphaPrefix string `json:"alpha_prefix,omitempty"`
	K           int    `json:"k,omitempty"`
	Beat        string `json:"beat,omitempty"`
	TTLMs       int64  `json:"ttl_ms,omitempty"`
	PubKey      string `json:"pubkey,omitempty"`
	Pred        string `json:"pred,omitempty"`
	Slot        uint32 `json:"slot,omitempty"`
}

type seekerResult struct {
	ID       uint64 `json:"id"`
	Nonce    string `json:"nonce"`
	Beta     string `json:"beta,omitempty"`
	Attempts uint64 `json:"attempts"`
	Seeker   string `json:"seeker"`
}

type seekerReport struct {
	ID       uint64 `json:"id"`
	Attempts uint64 `json:"attempts"`
	Seeker   string `json:"seeker"`
}

// seekerSolution is a verified result: the completed proof and its nonce,
// what mineParallel returns.
type seekerSolution struct {
	proof []byte
	nonce [txbuildercore.MineNonceLen]byte
}

type seekerStat struct {
	lastSeen time.Time
	attempts uint64  // cumulative
	hashrate float64 // EWMA over the seeker's own reports
}

// seekerServer holds the one current job and what verifying a result needs.
// The mining loop publishes a job per round and retires it when the round
// ends; handlers only ever read the job under the lock.
type seekerServer struct {
	prover *vrf.Prover
	pubKey string
	token  string

	mu       sync.Mutex
	changed  chan struct{} // closed and replaced whenever the job changes
	nextID   uint64
	job      seekerJob
	pred     base.OutputID
	slot     uint32
	k        int
	beat     []byte
	deadline time.Time
	solved   bool
	results  chan seekerSolution // per job, capacity 1
	seekers  map[string]*seekerStat

	attempts atomic.Uint64 // reported by seekers and not yet taken by the round
}

func newSeekerServer(prover *vrf.Prover, pubKey []byte, token string) *seekerServer {
	return &seekerServer{
		prover:  prover,
		pubKey:  hex.EncodeToString(pubKey),
		token:   token,
		changed: make(chan struct{}),
		// seeded with the start time so a restarted miner never reuses an id
		// a seeker may still hold
		nextID:  uint64(time.Now().UnixMilli()),
		seekers: make(map[string]*seekerStat),
	}
}

// listen opens the job server's listener. Port 0 picks a free one, the address
// of the listener says which.
func (s *seekerServer) listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

// serve blocks on the listener; it returns only on a listener error.
func (s *seekerServer) serve(ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return srv.Serve(ln)
}

func (s *seekerServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/seeker/job", s.auth(s.handleJob))
	mux.HandleFunc("/seeker/result", s.auth(s.handleResult))
	mux.HandleFunc("/seeker/report", s.auth(s.handleReport))
	return mux
}

// publish makes the target the current job and returns the channel on which
// the one accepted solution arrives.
func (s *seekerServer) publish(pred base.OutputID, slot uint32, k int, beat []byte, deadline time.Time) <-chan seekerSolution {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.job = seekerJob{
		ID:          s.nextID,
		AlphaPrefix: hex.EncodeToString(txbuildercore.MineVRFMessage(pred, slot, [txbuildercore.MineNonceLen]byte{})[:base.OutputIDLength+4]),
		K:           k,
		PubKey:      s.pubKey,
		Pred:        hex.EncodeToString(pred[:]),
		Slot:        slot,
	}
	if beat != nil {
		s.job.Beat = hex.EncodeToString(beat)
	}
	s.pred, s.slot, s.k, s.beat, s.deadline = pred, slot, k, beat, deadline
	s.solved = false
	s.results = make(chan seekerSolution, 1)
	s.wakeLocked()
	return s.results
}

// retire ends the current job: seekers polling for it get "no job" until the
// next round publishes.
func (s *seekerServer) retire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.job = seekerJob{}
	s.results = nil
	s.wakeLocked()
}

func (s *seekerServer) wakeLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// takeAttempts returns the attempts seekers reported since the last call and
// resets the counter; the round folds them into its total.
func (s *seekerServer) takeAttempts() uint64 {
	return s.attempts.Swap(0)
}

func (s *seekerServer) pendingAttempts() uint64 {
	return s.attempts.Load()
}

// summary is one line per seeker for the totals line, by name.
func (s *seekerServer) summary() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seekers) == 0 {
		return "no seeker has reported yet"
	}
	names := make([]string, 0, len(s.seekers))
	for n := range s.seekers {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		st := s.seekers[n]
		parts = append(parts, fmt.Sprintf("%s %s H/s (seen %v ago)", n, util.Th(uint64(st.hashrate)), time.Since(st.lastSeen).Round(time.Second)))
	}
	return strings.Join(parts, ", ")
}

func (s *seekerServer) auth(h http.HandlerFunc) http.HandlerFunc {
	if s.token == "" {
		return h
	}
	want := "Bearer " + s.token
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad token"})
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// handleJob answers GET /seeker/job?after=<id>&wait=<ms>: the current job,
// held back while it still equals `after`, for at most `wait` (capped).
func (s *seekerServer) handleJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	waitMs, _ := strconv.ParseInt(r.URL.Query().Get("wait"), 10, 64)
	wait := min(time.Duration(waitMs)*time.Millisecond, seekerMaxWait)
	timeout := time.After(wait)
	for {
		s.mu.Lock()
		job, changed, deadline := s.job, s.changed, s.deadline
		s.mu.Unlock()
		if job.ID != after || r.URL.Query().Get("after") == "" || wait <= 0 {
			if job.ID != 0 {
				job.TTLMs = max(0, time.Until(deadline).Milliseconds())
			}
			writeJSON(w, http.StatusOK, job)
			return
		}
		select {
		case <-changed:
		case <-timeout:
			wait = 0 // fall through to answer with whatever is current
		case <-r.Context().Done():
			return
		}
	}
}

// handleResult verifies a seeker's nonce against the current job and, when it
// holds, completes the proof and hands it to the round.
func (s *seekerServer) handleResult(w http.ResponseWriter, r *http.Request) {
	var res seekerResult
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&res); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json: " + err.Error()})
		return
	}
	s.credit(res.Seeker, res.Attempts)

	s.mu.Lock()
	stale := res.ID != s.job.ID || s.job.ID == 0 || s.solved || time.Now().After(s.deadline)
	pred, slot, k, beat := s.pred, s.slot, s.k, s.beat
	s.mu.Unlock()
	if stale {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "stale job"})
		return
	}

	sol, err := s.verify(res, pred, slot, k, beat)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// the job may have moved on during the verification
	if res.ID != s.job.ID || s.solved || time.Now().After(s.deadline) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "stale job"})
		return
	}
	s.solved = true
	s.results <- sol // capacity 1, written once per job
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
}

// verify is the computation of the spec's "work" section under the wallet key,
// then the proof. The seeker's own beta, when given, must match: the mismatch
// is the quickest diagnosis of a seeker bug.
func (s *seekerServer) verify(res seekerResult, pred base.OutputID, slot uint32, k int, beat []byte) (seekerSolution, error) {
	nb, err := hex.DecodeString(res.Nonce)
	if err != nil || len(nb) != txbuildercore.MineNonceLen {
		return seekerSolution{}, fmt.Errorf("nonce must be %d bytes hex", txbuildercore.MineNonceLen)
	}
	var nonce [txbuildercore.MineNonceLen]byte
	copy(nonce[:], nb)
	beta, st, err := s.prover.Output(txbuildercore.MineVRFMessage(pred, slot, nonce))
	if err != nil {
		return seekerSolution{}, err
	}
	if res.Beta != "" {
		if claimed, err := hex.DecodeString(res.Beta); err != nil || !bytes.Equal(claimed, beta) {
			return seekerSolution{}, fmt.Errorf("beta mismatch: the miner computes %s", hex.EncodeToString(beta))
		}
	}
	if z := trailingZeroBits(beta); z < k {
		return seekerSolution{}, fmt.Errorf("insufficient zero bits: %d < %d", z, k)
	}
	if beat != nil && bytes.Compare(beta, beat) >= 0 {
		return seekerSolution{}, errors.New("output is not below beat")
	}
	proof, err := s.prover.ProofFor(st)
	if err != nil {
		return seekerSolution{}, err
	}
	return seekerSolution{proof: proof, nonce: nonce}, nil
}

func (s *seekerServer) handleReport(w http.ResponseWriter, r *http.Request) {
	var rep seekerReport
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&rep); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json: " + err.Error()})
		return
	}
	s.credit(rep.Seeker, rep.Attempts)
	writeJSON(w, http.StatusOK, map[string]string{})
}

// credit books attempts to the round and to the seeker's own rate.
func (s *seekerServer) credit(name string, attempts uint64) {
	s.attempts.Add(attempts)
	if name == "" {
		name = "unnamed"
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.seekers[name]
	if !ok {
		st = &seekerStat{lastSeen: now}
		s.seekers[name] = st
	}
	st.hashrate = updateHashrate(st.hashrate, attempts, now.Sub(st.lastSeen))
	st.attempts += attempts
	st.lastSeen = now
}
