package multispam

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
)

// spentEntry tracks a UTXO that was consumed by a submitted transaction.
type spentEntry struct {
	TxID          base.TransactionID
	SubmittedSlot uint32
}

// Sender is an autonomous goroutine that continuously sends transactions from one account.
type Sender struct {
	name       string
	index      int // position in the sender list (for "next" strategy)
	privateKey ed25519.PrivateKey
	account    ledger.SigLock
	holderID   base.HolderID

	cfg       *Config
	hosts     []HostConfig
	hostIdx   int
	seqPicker *SequencerPicker
	targets   []ledger.SigLock // all sender addresses for target strategies
	spentSet  map[base.OutputID]spentEntry
	metrics   *SenderMetrics
	logFunc   func(format string, args ...any)
}

// SenderMetrics holds per-sender counters, read atomically by the coordinator.
type SenderMetrics struct {
	TxSent      atomic.Int64
	TxFailed    atomic.Int64
	LastBalance atomic.Uint64
}

// SenderParams holds everything needed to create a Sender.
type SenderParams struct {
	Name       string
	Index      int
	PrivateKey ed25519.PrivateKey
	Config     *Config
	SeqPicker  *SequencerPicker
	Targets    []ledger.SigLock
	LogFunc    func(format string, args ...any)
}

func NewSender(par SenderParams) *Sender {
	account := ledger.SigLockFromED25519PrivateKey(par.PrivateKey)
	return &Sender{
		name:       par.Name,
		index:      par.Index,
		privateKey: par.PrivateKey,
		account:    account,
		holderID:   base.HolderIDFromPublicKey(base.SignatureTypeED25519, par.PrivateKey.Public().(ed25519.PublicKey)),
		cfg:        par.Config,
		hosts:      par.Config.APIHosts,
		seqPicker:  par.SeqPicker,
		targets:    par.Targets,
		spentSet:   make(map[base.OutputID]spentEntry),
		metrics:    &SenderMetrics{},
		logFunc:    par.LogFunc,
	}
}

func (s *Sender) Metrics() *SenderMetrics { return s.metrics }
func (s *Sender) Name() string            { return s.name }

// Run is the main sender loop. Blocks until context is cancelled.
func (s *Sender) Run(ctx context.Context) {
	pace := int(ledger.L(base.MaxSlot).TransactionPace)
	paceDuration := time.Duration(pace) * ledger.TickDuration()
	mindRateControl := s.cfg.IsMindRateControl()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		sent := s.doRound(pace)
		if !sent || mindRateControl {
			// Wait pace duration: either nothing to spend, or respecting rate control
			select {
			case <-ctx.Done():
				return
			case <-time.After(paceDuration):
			}
		}
	}
}

// doRound performs one iteration: query outputs, classify, build and submit.
// Returns true if at least one transaction was submitted.
func (s *Sender) doRound(pace int) bool {
	clnt := s.client()

	// Step 1: Query LRB outputs
	outs, _, totalBalance, err := clnt.GetTransferableOutputs(s.account, 256)
	if err != nil {
		s.log("error fetching outputs: %v", err)
		s.rotateHost()
		return false
	}

	s.metrics.LastBalance.Store(totalBalance)

	// Build a set of output IDs in current LRB for fast lookup
	lrbOutputs := make(map[base.OutputID]*ledger.OutputWithID, len(outs))
	for _, o := range outs {
		lrbOutputs[o.ID] = o
	}

	// Step 2: Classify outputs and maintain spentSet
	currentSlot := ledger.TimeNow().Slot
	s.classifyOutputs(lrbOutputs, currentSlot, clnt)

	// Collect available outputs (in LRB and not in spentSet)
	var available []*ledger.OutputWithID
	var availableBalance uint64
	for _, o := range outs {
		if _, spent := s.spentSet[o.ID]; !spent {
			available = append(available, o)
			availableBalance += o.Output.TokenBalance()
		}
	}

	if len(available) == 0 {
		return false
	}

	// Step 3: Get sequencer for tag-along
	seqInfo, ok := s.seqPicker.Next()
	if !ok {
		s.log("no sequencers available")
		return false
	}

	// Step 4: Check minimum balance
	transferAmount := s.cfg.Global.TransferAmount
	tagAlongFee := seqInfo.Fee
	// Estimate storage deposit for a sigLock output (~45 bytes → ~13.6M tokens minimum)
	// Use a conservative minimum; the actual check happens in ProduceOutput
	minNeeded := transferAmount + tagAlongFee + transferAmount // remainder needs at least storage deposit worth
	if availableBalance < minNeeded {
		return false
	}

	// Step 5: Build and submit batch
	return s.buildAndSubmitBatch(available, pace, seqInfo, clnt)
}

// classifyOutputs updates the spentSet based on current LRB state.
func (s *Sender) classifyOutputs(lrbOutputs map[base.OutputID]*ledger.OutputWithID, currentSlot uint32, clnt *client.APIClient) {
	finalitySlots := uint32(s.cfg.Global.FinalityTimeoutSlots)

	for oid, entry := range s.spentSet {
		if _, inLRB := lrbOutputs[oid]; !inLRB {
			// Output no longer in LRB — the spending tx was finalized (output consumed)
			// or the output itself was consumed by someone else. Either way, remove.
			delete(s.spentSet, oid)
			continue
		}
		// Output is still in LRB but we marked it spent. Check if spending tx finalized.
		_, foundAtDepth, err := clnt.CheckTransactionIDInLRB(entry.TxID, 1)
		if err != nil {
			// API error — leave as is for now
			continue
		}
		if foundAtDepth >= 0 {
			// Spending tx is in LRB — it's finalized, output should disappear soon
			delete(s.spentSet, oid)
			continue
		}
		// Spending tx not in LRB. Check if enough time has passed to reclaim.
		if currentSlot-entry.SubmittedSlot >= finalitySlots {
			// Reclaim: allow re-spending this output
			delete(s.spentSet, oid)
		}
	}
}

func (s *Sender) buildAndSubmitBatch(available []*ledger.OutputWithID, pace int, seqInfo SequencerInfo, clnt *client.APIClient) bool {
	batchSize := s.cfg.Global.BatchSize
	anySent := false

	// For batch > 1, we chain transactions: each tx consumes the remainder of the previous.
	// The first tx uses available UTXOs from the LRB.
	// Subsequent txs in the batch consume only the remainder output from the previous tx.

	currentInputs := available
	var remainderOutput *ledger.OutputWithID // output from previous tx in the batch

	for txIdx := 0; txIdx < batchSize; txIdx++ {
		isLastInBatch := txIdx == batchSize-1

		var inputs []*ledger.OutputWithID
		if remainderOutput != nil {
			inputs = []*ledger.OutputWithID{remainderOutput}
		} else {
			inputs = currentInputs
		}

		txBytes, txID, remainder, err := s.buildOneTx(inputs, pace, seqInfo, isLastInBatch)
		if err != nil {
			s.log("build tx error: %v", err)
			s.metrics.TxFailed.Add(1)
			break
		}

		// Submit
		err = clnt.SubmitTransaction(txBytes)
		if err != nil {
			s.log("submit error: %v", err)
			s.metrics.TxFailed.Add(1)
			s.rotateHost()
			// Retry with next host
			clnt = s.client()
			err = clnt.SubmitTransaction(txBytes)
			if err != nil {
				s.log("submit retry error: %v", err)
				break
			}
		}

		// Mark consumed inputs as spent
		submittedSlot := txID.Timestamp().Slot
		for _, inp := range inputs {
			s.spentSet[inp.ID] = spentEntry{
				TxID:          txID,
				SubmittedSlot: submittedSlot,
			}
		}

		s.metrics.TxSent.Add(1)
		s.nextHost()
		anySent = true

		// Set up remainder for next tx in batch
		remainderOutput = remainder
		if remainderOutput == nil {
			break // no remainder means no balance left
		}
	}

	return anySent
}

// buildOneTx constructs a single transfer transaction.
// Returns txBytes, txID, remainder output (for chaining), or error.
func (s *Sender) buildOneTx(inputs []*ledger.OutputWithID, pace int, seqInfo SequencerInfo, includeTagAlong bool) ([]byte, base.TransactionID, *ledger.OutputWithID, error) {
	txb := txbuilder.New()

	inTotal, inTs, err := txb.ConsumeOutputsNoUnlock(inputs...)
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}

	// Timestamp: max input ts + pace
	ts := inTs.AddTicks(pace)
	now := ledger.TimeNow()
	if ts.Before(now) {
		ts = now
	}

	if !ledger.ValidTransactionPace(inTs, ts) {
		return nil, base.TransactionID{}, nil, fmt.Errorf("pace violation")
	}

	// Unlock params
	for i := range inputs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			_ = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
		}
	}

	transferAmount := s.cfg.Global.TransferAmount
	tagAlongFee := uint64(0)
	if includeTagAlong {
		tagAlongFee = seqInfo.Fee
	}

	if inTotal < transferAmount+tagAlongFee {
		return nil, base.TransactionID{}, nil, fmt.Errorf("insufficient balance: have %d, need %d", inTotal, transferAmount+tagAlongFee)
	}

	// Target output
	targetLock := s.resolveTarget()
	targetOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(transferAmount).WithLock(targetLock)
	})
	if _, err = txb.ProduceOutput(targetOut); err != nil {
		return nil, base.TransactionID{}, nil, fmt.Errorf("produce target: %w", err)
	}

	// Tag-along output (only on last tx in batch)
	if tagAlongFee > 0 {
		taOut := ledger.NewTagAlongOutput(tagAlongFee, seqInfo.ChainID, s.holderID)
		if _, err = txb.ProduceOutput(taOut); err != nil {
			return nil, base.TransactionID{}, nil, fmt.Errorf("produce tag-along: %w", err)
		}
	}

	// Remainder output
	var remainderOut *ledger.OutputWithID
	remainderAmount := inTotal - transferAmount - tagAlongFee
	if remainderAmount > 0 {
		remOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(remainderAmount).WithLock(s.account)
		})
		remIdx, err := txb.ProduceOutput(remOut)
		if err != nil {
			return nil, base.TransactionID{}, nil, fmt.Errorf("produce remainder: %w", err)
		}
		// We'll fill in the OutputID after we know the txID
		_ = remIdx
	}

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(s.privateKey)

	txBytes, txID, txStr, err := txb.BytesWithValidation()
	if err != nil {
		return nil, base.TransactionID{}, nil, fmt.Errorf("validation: %w\n%s", err, txStr)
	}

	// Build remainder OutputWithID for chaining
	if remainderAmount > 0 {
		// Remainder is the last produced output
		numOutputs := txb.NumOutputs()
		remOID := base.MustNewOutputID(txID, byte(numOutputs-1))
		remOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(remainderAmount).WithLock(s.account)
		})
		remainderOut = &ledger.OutputWithID{
			ID:     remOID,
			Output: remOut,
		}
	}

	return txBytes, txID, remainderOut, nil
}

// resolveTarget picks the target address based on strategy.
func (s *Sender) resolveTarget() ledger.Lock {
	switch s.cfg.Global.TargetStrategy {
	case StrategyNext:
		nextIdx := (s.index + 1) % len(s.targets)
		return s.targets[nextIdx]
	case StrategyRandom:
		idx := rand.Intn(len(s.targets))
		return s.targets[idx]
	default: // "self"
		return s.account
	}
}

func (s *Sender) client() *client.APIClient {
	h := s.hosts[s.hostIdx]
	return client.NewWithGoogleDNS(h.URL, h.Timeout)
}

// nextHost advances to the next host according to the configured strategy.
func (s *Sender) nextHost() {
	if len(s.hosts) <= 1 {
		return
	}
	switch s.cfg.Global.HostStrategy {
	case StrategyRandom:
		s.hostIdx = rand.Intn(len(s.hosts))
	default: // "next" — round-robin
		s.hostIdx = (s.hostIdx + 1) % len(s.hosts)
	}
}

// rotateHost advances to a different host on error (always moves forward to avoid the failing host).
func (s *Sender) rotateHost() {
	if len(s.hosts) <= 1 {
		return
	}
	s.hostIdx = (s.hostIdx + 1) % len(s.hosts)
}

func (s *Sender) log(format string, args ...any) {
	if s.logFunc != nil {
		msg := fmt.Sprintf(format, args...)
		s.logFunc("[%s] %s", s.name, msg)
	}
}
