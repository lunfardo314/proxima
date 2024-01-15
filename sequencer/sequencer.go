package sequencer

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/sequencer/factory"
	"go.uber.org/zap"
)

type (
	Sequencer struct {
		*workflow.Workflow
		name          string
		sequencerID   ledger.ChainID
		controllerKey ed25519.PrivateKey
		config        *ConfigOptions
		log           *zap.SugaredLogger
		factory       *factory.MilestoneFactory
		//exit     atomic.Bool
		//stopWG   sync.WaitGroup
		//stopOnce sync.Once
		//
		//onMilestoneSubmittedMutex sync.RWMutex
		//onMilestoneSubmitted      func(seq *Sequencer, vid *vertex.WrappedOutput)
		//
		//infoMutex      sync.RWMutex
		//info           Info
		//traceNAhead    atomic.Int64
		//prevTimeTarget ledger.LogicalTime
	}

	Info struct {
		In                     int
		Out                    int
		InflationAmount        uint64
		NumConsumedFeeOutputs  int
		NumFeeOutputsInTippool int
		NumOtherMsInTippool    int
		LedgerCoverage         uint64
		PrevLedgerCoverage     uint64
		AvgProposalDuration    time.Duration
		NumProposals           int
	}
)

func New(glb *workflow.Workflow, seqID ledger.ChainID, controllerKey ed25519.PrivateKey, ctx context.Context, opts ...ConfigOption) (*Sequencer, error) {
	cfg := makeConfig(opts...)
	ret := &Sequencer{
		Workflow:      glb,
		name:          cfg.SequencerName,
		sequencerID:   seqID,
		controllerKey: controllerKey,
		config:        cfg,
		log:           glb.Log().Named(fmt.Sprintf("%s-%s", cfg.SequencerName, seqID.StringVeryShort())),
	}
	var err error
	if ret.factory, err = factory.New(ret, cfg.MaxFeeInputs); err != nil {
		return nil, err
	}
	return ret, nil
}

func (s *Sequencer) SequencerID() ledger.ChainID {
	return s.sequencerID
}

func (s *Sequencer) ControllerPrivateKey() ed25519.PrivateKey {
	return s.controllerKey
}

func (s *Sequencer) SequencerName() string {
	return s.name
}
