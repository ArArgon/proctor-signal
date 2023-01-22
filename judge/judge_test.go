package judge

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zapcore"

	"proctor-signal/external/gojudge"
	"proctor-signal/resource"

	judgeconfig "github.com/criyle/go-judge/cmd/executorserver/config"

	"github.com/samber/lo"
	"go.uber.org/zap"
)

func TestExecuteCommand(t *testing.T) {
	// Init logger.
	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	config.Level.SetLevel(zap.InfoLevel)
	logger := lo.Must(config.Build())

	// Prepare tmp dir.
	loc, err := os.MkdirTemp(os.TempDir(), "signal")
	assert.NoError(t, err)
	defer func() {
		assert.NoError(t, os.RemoveAll(loc))
	}()

	// Init fs.
	fs := lo.Must(resource.NewFileStore(logger, loc))

	conf := loadConf()
	b := gojudge.NewEnvBuilder(conf)
	envPool := gojudge.NewEnvPool(b, false)
	gojudge.Prefork(envPool, conf.PreFork)
	worker := gojudge.NewWorker(conf, envPool, fs)
	m := NewJudgeManager(worker)

	fmt.Println(m.ExecuteCommand(context.Background(), "echo 114514"))
}

func loadConf() *judgeconfig.Config {
	var conf judgeconfig.Config
	if err := conf.Load(); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		log.Fatalln("load config failed ", err)
	}
	return &conf
}
