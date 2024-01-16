package sequencer

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/sequencer/factory"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"go.uber.org/zap"
)

type (
	Sequencer struct {
		*workflow.Workflow
		sequencerID   ledger.ChainID
		controllerKey ed25519.PrivateKey
		config        *ConfigOptions
		log           *zap.SugaredLogger
		factory       *factory.MilestoneFactory
		waitStop      sync.WaitGroup
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

func New(glb *workflow.Workflow, seqID ledger.ChainID, controllerKey ed25519.PrivateKey, opts ...ConfigOption) (*Sequencer, error) {
	cfg := makeConfig(opts...)
	ret := &Sequencer{
		Workflow:      glb,
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

func (s *Sequencer) Start(ctx context.Context) {
	s.waitStop.Add(1)
	util.RunWrappedRoutine(s.config.SequencerName+"[mainLoop]", func() {
		s.mainLoop(ctx)
		s.waitStop.Done()
	}, func(err error) {
		s.log.Fatal(err)
	}, common.ErrDBUnavailable)
}

func (s *Sequencer) WaitStop() {
	s.waitStop.Wait()
}

func (s *Sequencer) SequencerID() ledger.ChainID {
	return s.sequencerID
}

func (s *Sequencer) ControllerPrivateKey() ed25519.PrivateKey {
	return s.controllerKey
}

func (s *Sequencer) SequencerName() string {
	return s.config.SequencerName
}

func (s *Sequencer) Log() *zap.SugaredLogger {
	return s.log
}

func (s *Sequencer) Tracef(tag string, format string, args ...any) {
	s.Workflow.TraceLog(s.log, tag, format, args...)
}

func (s *Sequencer) mainLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.log.Info("sequencer STOPPING..")
			return
		default:
			s.doSequencing()
		}
	}
}

func (s *Sequencer) doSequencing() {
	s.log.Info("doSequencing")
	time.Sleep(time.Second)
}
