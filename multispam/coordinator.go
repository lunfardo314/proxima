package multispam

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
)

// Coordinator manages multiple senders, sequencer discovery, and display.
type Coordinator struct {
	cfg     *Config
	senders []*Sender
	seqReg  *SequencerRegistry
	logFunc func(format string, args ...any)

	maxDuration     time.Duration
	maxTransactions int64
}

type CoordinatorParams struct {
	Config          *Config
	NumSenders      int // 0 means all
	MaxDuration     time.Duration
	MaxTransactions int64
	LogFunc         func(format string, args ...any)
}

func NewCoordinator(par CoordinatorParams) (*Coordinator, error) {
	cfg := par.Config
	numSenders := par.NumSenders
	if numSenders <= 0 || numSenders > len(cfg.Senders) {
		numSenders = len(cfg.Senders)
	}

	seqReg := NewSequencerRegistry()

	// Load all sender keys and addresses (for target resolution)
	allAddrs := make([]ledger.SigLock, numSenders)
	keys := make([]ed25519.PrivateKey, numSenders)
	for i := 0; i < numSenders; i++ {
		privKey, err := LoadSenderKey(cfg.Senders[i].KeyFile)
		if err != nil {
			return nil, fmt.Errorf("loading key for sender '%s': %w", cfg.Senders[i].Name, err)
		}
		keys[i] = privKey
		allAddrs[i] = ledger.SigLockFromED25519PrivateKey(privKey)
	}

	senders := make([]*Sender, numSenders)
	for i := 0; i < numSenders; i++ {
		senders[i] = NewSender(SenderParams{
			Name:       cfg.Senders[i].Name,
			Index:      i,
			PrivateKey: keys[i],
			Config:     cfg,
			SeqPicker:  NewSequencerPicker(seqReg, cfg.Global.SequencerStrategy),
			Targets:    allAddrs,
			LogFunc:    par.LogFunc,
		})
	}

	return &Coordinator{
		cfg:             cfg,
		senders:         senders,
		seqReg:          seqReg,
		logFunc:         par.LogFunc,
		maxDuration:     par.MaxDuration,
		maxTransactions: par.MaxTransactions,
	}, nil
}

// Run starts all senders and the display loop. Blocks until context is cancelled
// or limits are reached.
func (c *Coordinator) Run(ctx context.Context) error {
	// Initial sequencer discovery
	clnt := c.firstClient()
	if err := c.seqReg.Refresh(clnt); err != nil {
		return fmt.Errorf("initial sequencer discovery failed: %w", err)
	}
	if c.seqReg.Count() == 0 {
		return fmt.Errorf("no active sequencers found")
	}
	c.log("discovered %d sequencer(s)", c.seqReg.Count())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Apply max duration
	if c.maxDuration > 0 {
		ctx, cancel = context.WithTimeout(ctx, c.maxDuration)
		defer cancel()
	}

	// Start sender goroutines
	for _, sender := range c.senders {
		go sender.Run(ctx)
	}
	c.log("started %d sender(s), strategy: %s", len(c.senders), c.cfg.Global.TargetStrategy)

	// Display + sequencer refresh loop
	slotDuration := ledger.SlotDuration()
	ticker := time.NewTicker(slotDuration)
	defer ticker.Stop()

	startTime := time.Now()
	var prevTotalSent int64

	for {
		select {
		case <-ctx.Done():
			c.printFinalStats(startTime)
			return nil
		case <-ticker.C:
			// Refresh sequencers periodically
			_ = c.seqReg.Refresh(c.firstClient())

			// Check transaction limit
			totalSent := c.totalSent()
			if c.maxTransactions > 0 && totalSent >= c.maxTransactions {
				c.log("transaction limit reached (%d)", totalSent)
				c.printFinalStats(startTime)
				return nil
			}

			// Display stats
			elapsed := time.Since(startTime).Seconds()
			tps := float64(totalSent) / elapsed
			slotTxs := totalSent - prevTotalSent
			totalFailed := c.totalFailed()

			var senderStats strings.Builder
			for _, s := range c.senders {
				m := s.Metrics()
				fmt.Fprintf(&senderStats, "  %s: sent=%d bal=%s",
					s.Name(),
					m.TxSent.Load(),
					formatBalance(m.LastBalance.Load()),
				)
			}

			fmt.Printf("\r\033[K[%.0fs] TPS: %.1f | sent: %d (+%d) | failed: %d\n%s\n",
				elapsed, tps, totalSent, slotTxs, totalFailed, senderStats.String())

			prevTotalSent = totalSent
		}
	}
}

func (c *Coordinator) totalSent() int64 {
	var total int64
	for _, s := range c.senders {
		total += s.Metrics().TxSent.Load()
	}
	return total
}

func (c *Coordinator) totalFailed() int64 {
	var total int64
	for _, s := range c.senders {
		total += s.Metrics().TxFailed.Load()
	}
	return total
}

func (c *Coordinator) printFinalStats(startTime time.Time) {
	elapsed := time.Since(startTime).Seconds()
	totalSent := c.totalSent()
	totalFailed := c.totalFailed()
	tps := float64(totalSent) / elapsed

	c.log("--- final stats ---")
	c.log("duration: %.1fs, total sent: %d, failed: %d, avg TPS: %.1f",
		elapsed, totalSent, totalFailed, tps)
	for _, s := range c.senders {
		m := s.Metrics()
		c.log("  %s: sent=%d failed=%d balance=%s",
			s.Name(), m.TxSent.Load(), m.TxFailed.Load(), formatBalance(m.LastBalance.Load()))
	}
}

func (c *Coordinator) firstClient() *client.APIClient {
	h := c.cfg.APIHosts[0]
	return client.NewWithGoogleDNS(h.URL, h.Timeout)
}

func (c *Coordinator) log(format string, args ...any) {
	if c.logFunc != nil {
		c.logFunc(format, args...)
	}
}

func formatBalance(b uint64) string {
	switch {
	case b >= 1_000_000_000:
		return fmt.Sprintf("%.1fG", float64(b)/1_000_000_000)
	case b >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(b)/1_000_000)
	case b >= 1_000:
		return fmt.Sprintf("%.1fK", float64(b)/1_000)
	default:
		return fmt.Sprintf("%d", b)
	}
}
