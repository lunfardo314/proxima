package node

import (
	"os"

	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

func init() {
	pflag.String("logger.level", "info", "log level")
	pflag.String("logger.timelayout", "2006-01-02 15:04:05.000", "time format")
	pflag.String("logger.output", "stdout", "a list where to write log")

	pflag.String("multistate.name", "proximadb", "name of the multi-state database")
	pflag.String("txstore.type", "dummy", "one of: db | dummy | url")
	pflag.String("txstore.name", "", "depending on type: name of the db or url")
}

func initConfig(log *zap.SugaredLogger) {
	pflag.Parse()
	err := viper.BindPFlags(pflag.CommandLine)
	util.AssertNoError(err)

	viper.SetConfigName(".proxima")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	err = viper.ReadInConfig()
	util.AssertNoError(err)

	if viper.GetString("multistate.name") == "" {
		log.Errorf("multistate database not specified")
		os.Exit(1)
	}
	log.Infof("multistate database is '%s'", viper.GetString("multistate.name"))

	switch viper.GetString("txstore.type") {
	case "dummy":
		log.Infof("transaction store is 'dummy'")
	case "db":
		name := viper.GetString("txstore.name")
		log.Infof("transaction store database name is '%s'", name)
		if name == "" {
			log.Errorf("transaction store database name not specified")
			os.Exit(1)
		}
	case "url":
		name := viper.GetString("txstore.name")
		log.Infof("transaction store URL is '%s'", name)
		if name == "" {
			log.Errorf("transaction store URL not specified")
		}
	default:
		log.Errorf("transaction store type '%s' is wrong", viper.GetString("txstore.type"))
		os.Exit(1)
	}
}
