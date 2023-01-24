package judge

import (
	"context"
	"flag"
	"log"
	"os"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"

	"proctor-signal/external/gojudge"
	"proctor-signal/model"
	"proctor-signal/resource"

	judgeconfig "github.com/criyle/go-judge/cmd/executorserver/config"
	"github.com/stretchr/testify/assert"

	"github.com/criyle/go-sandbox/container"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

var judgeManger *Manager

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

	// init judge
	LoadLanguageConfig("../language.yaml")
	judgeManger = NewJudgeManager(worker)

	os.Exit(m.Run())
}

var cacheFiles map[string]string

func TestCompile(t *testing.T) {
	p := &model.Problem{DefaultTimeLimit: uint32(time.Second), DefaultSpaceLimit: 104857600}
	ctx := context.Background()
	cacheFiles = make(map[string]string)

	for language, conf := range languageConfig {
		// just for C
		if language != "c" {
			continue
		}

		t.Run(language, func(t *testing.T) {
			codes, err := os.ReadFile("tests/" + conf.SourceName)
			assert.NoError(t, err)
			sub := &model.Submission{Language: language, SourceCode: codes}

			compileRes, err := judgeManger.Compile(ctx, p, sub)
			assert.NotNil(t, compileRes)
			assert.NoError(t, err)
			if compileRes.Status >= 4 {
				t.Errorf("failed to finish compile: compileRes.Status >= 4, compileRes: %v", compileRes)
			}

			id, ok := compileRes.ArtifactFileIDs[conf.ArtifactName]
			if !ok {
				t.Errorf("failed to finish compile: failed to cache fille, compileRes: %v", compileRes)
			}
			cacheFiles[language] = id
		})
	}
}

func TestExecuteFile(t *testing.T) {
	p := &model.Problem{DefaultTimeLimit: uint32(time.Second), DefaultSpaceLimit: 104857600}
	ctx := context.Background()
	stdin, err := os.ReadFile("tests/input")
	assert.NoError(t, err, "failed to read tests/input")
	stdout, err := os.ReadFile("tests/output")
	assert.NoError(t, err, "failed to read tests/output")

	for language, conf := range languageConfig {
		// just for C
		if language != "c" {
			continue
		}

		t.Run(language, func(t *testing.T) {
			executeRes, err := judgeManger.ExecuteFile(ctx, conf.ArtifactName, cacheFiles[language], stdin, p)
			assert.NoError(t, err)
			if executeRes.ExitStatus != 0 {
				t.Errorf("failed to execute: executeRes.ExitStatus != 0, executeRes: %v", executeRes)
			}
			assert.Equal(t, string(stdout), executeRes.Output)
		})
	}
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
