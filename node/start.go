package node

import (
	"strings"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type ProximaNode struct {
	Log       *zap.SugaredLogger
	DB        *badger.DB
	TxStoreDB *badger.DB
}

var Node *ProximaNode

func Start() {
	log := newBootstrapLogger()
	log.Info(general.BannerString())

	initConfig(log)

	Node = &ProximaNode{
		Log: newLogger(),
	}

	Node.startup()

	Node.Log.Info("Proxima node has been started successfully")
	Node.Log.Debug("running in debug mode")
}

func (p *ProximaNode) startup() {
	p.Log.Info("starting up..")

}

func (p *ProximaNode) cleanup() {
	p.Log.Info("cleaning up..")
	if p.DB != nil {
		p.DB.Close()
	}
	if p.TxStoreDB != nil {
		p.TxStoreDB.Close()
	}
}

func (p *ProximaNode) GetMultiStateDBName() string {
	return viper.GetString("multistate.name")
}

func newBootstrapLogger() *zap.SugaredLogger {
	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(zap.InfoLevel),
		Development:      true,
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
		DisableCaller:    true,
	}
	cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("2006-01-02 15:04:05.000")
	log, err := cfg.Build()
	common.AssertNoError(err)
	log.Core()
	log = log.WithOptions(zap.IncreaseLevel(zap.InfoLevel), zap.AddStacktrace(zapcore.FatalLevel))
	return log.Sugar().Named("-boot-")
}

func newLogger() *zap.SugaredLogger {
	logLevel := zapcore.InfoLevel
	if viper.GetString("logger.level") == "debug" {
		logLevel = zapcore.DebugLevel
	}

	outputStr := viper.GetString("logger.output")
	outputs := strings.Split(outputStr, ",")
	if util.Find(outputs, "stdout") < 0 {
		outputs = append(outputs, "stdout")
	}

	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(logLevel),
		Development:      true,
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      outputs,
		ErrorOutputPaths: outputs,
		DisableCaller:    true,
	}
	cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(viper.GetString("logger.timelayout"))
	log, err := cfg.Build()
	common.AssertNoError(err)
	log.Core()
	log = log.WithOptions(zap.IncreaseLevel(logLevel), zap.AddStacktrace(zapcore.FatalLevel))

	return log.Sugar().Named("-top-")
}
