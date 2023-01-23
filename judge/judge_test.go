package judge_test

import (
	"context"
	"flag"
	"log"
	"os"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"

	"proctor-signal/external/gojudge"
	"proctor-signal/judge"
	"proctor-signal/model"
	"proctor-signal/resource"

	judgeconfig "github.com/criyle/go-judge/cmd/executorserver/config"
	"github.com/stretchr/testify/assert"

	"github.com/criyle/go-sandbox/container"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

var judgeManger *judge.Manager

func TestMain(m *testing.M) {
	// Init logger.
	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	config.Level.SetLevel(zap.InfoLevel)
	logger := lo.Must(config.Build())
	gojudge.Init(logger)

	// Prepare tmp dir.
	loc, err := os.MkdirTemp(os.TempDir(), "signal")
	if err != nil {
		panic("faild to prepare tmp dir: " + err.Error())
	}
	defer func() { os.RemoveAll(loc) }()

	// Init fs.
	fs := lo.Must(resource.NewFileStore(logger, loc))

	// Init gojudge
	err = container.Init()
	if err != nil {
		panic("faild to init container: " + err.Error())
	}

	conf := loadConf()
	b := gojudge.NewEnvBuilder(conf)
	envPool := gojudge.NewEnvPool(b, false)
	gojudge.Prefork(envPool, conf.PreFork)
	worker := gojudge.NewWorker(conf, envPool, fs)
	judgeManger = judge.NewJudgeManager(worker)

	os.Exit(m.Run())
}

// func TestExecuteCommand(t *testing.T) {
// 	t.Log(judgeManger.ExecuteCommand(context.Background(), "echo 114514"))
// }

func TestCompile(t *testing.T) {
	p := &model.Problem{DefaultTimeLimit: uint32(time.Second), DefaultSpaceLimit: 104857600}
	codes, err := os.ReadFile("tests/source.c")
	assert.NoError(t, err)
	sub := &model.Submission{Language: "c", SourceCode: codes}

	judgeManger.Compile(context.Background(), p, sub)
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
