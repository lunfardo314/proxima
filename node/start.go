package node

import (
	"strings"

	"github.com/dgraph-io/badger/v4"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type ProximaNode struct {
	Log       *zap.SugaredLogger
	DB        *badger.DB
	TxStoreDB *badger.DB
}

func Start() *ProximaNode {
	log := newBootstrapLogger()
	log.Info(general.BannerString())

	initConfig(log)

	ret := &ProximaNode{
		Log: newLogger(),
	}

	ret.startup()

	ret.Log.Infof("Proxima node has been started successfully with multistate db '%s'", ret.GetMultiStateDBName())
	ret.Log.Debug("running in debug mode")

	return ret
}

func (p *ProximaNode) startup() {
	p.Log.Info("starting up..")

}

func (p *ProximaNode) Stop() {
	p.Log.Info("stopping the node..")
	if p.DB != nil {
		p.DB.Close()
	}
	if p.TxStoreDB != nil {
		p.TxStoreDB.Close()
	}
	p.Log.Info("node stopped")
}

func (p *ProximaNode) GetMultiStateDBName() string {
	return viper.GetString("multistate.name")
}

func newBootstrapLogger() *zap.SugaredLogger {
	return general.NewLogger("-boot", zap.InfoLevel, []string{"stderr"}, "")
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

	return general.NewLogger("-top", logLevel, outputs, viper.GetString("logger.timelayout"))
}
