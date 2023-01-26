package judge

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"go.uber.org/zap/zapcore"

	"proctor-signal/external/gojudge"
	"proctor-signal/model"
	"proctor-signal/resource"

	judgeconfig "github.com/criyle/go-judge/cmd/executorserver/config"
	"github.com/criyle/go-judge/worker"
	"github.com/stretchr/testify/assert"

	"github.com/criyle/go-sandbox/container"
	"github.com/criyle/go-sandbox/runner"
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

	// init judgeManger
	LoadLanguageConfig("../language.yaml")
	judgeManger = NewJudgeManager(worker)
	judgeManger.fs = fs

	os.Exit(m.Run())
}

// func TestWokerExecute(t *testing.T) {
// 	ctx := context.Background()

// 	res := <-judgeManger.worker.Execute(ctx, &worker.Request{
// 		Cmd: []worker.Cmd{{
// 			Env:         []string{"PATH=/usr/bin:/bin"},
// 			Args:        []string{"cat", "tmp.txt"},
// 			CPULimit:    time.Second,
// 			MemoryLimit: 104857600,
// 			ProcLimit:   50,
// 			Files: []worker.CmdFile{
// 				&worker.MemoryFile{Content: []byte("")},
// 				&worker.Collector{Name: "stdout", Max: 10240},
// 				&worker.Collector{Name: "stderr", Max: 10240},
// 			},
// 			CopyIn: map[string]worker.CmdFile{
// 				"tmp.txt": &worker.MemoryFile{Content: []byte("114514")},
// 			},
// 			CopyOut: []worker.CmdCopyOutFile{
// 				{Name: "stdout", Optional: true},
// 				{Name: "stderr", Optional: true},
// 			},
// 			CopyOutCached: []worker.CmdCopyOutFile{
// 				{Name: "tmp.txt", Optional: true},
// 			},
// 		}},
// 	})

// 	executeRes := &ExecuteRes{
// 		Status:     res.Results[0].Status,
// 		ExitStatus: res.Results[0].ExitStatus,
// 		Error:      res.Results[0].Error,
// 	}
// 	assert.NoError(t, res.Error)

// 	// read execute output
// 	var executeOutput []byte
// 	var err error
// 	if res.Results[0].ExitStatus == 0 {
// 		_, err = res.Results[0].Files["stdout"].Seek(0, 0)
// 		assert.NoError(t, err, "failed to reseek execute stdout: ")

// 		executeOutput, err = io.ReadAll(res.Results[0].Files["stdout"])
// 		assert.NoError(t, err, "failed to read execute stdout: ")
// 	} else {
// 		_, err = res.Results[0].Files["stderr"].Seek(0, 0)
// 		assert.NoError(t, err, "failed to reseek execute stderr: ")

// 		executeOutput, err = io.ReadAll(res.Results[0].Files["stderr"])
// 		assert.NoError(t, err, "failed to read execute stderr: ")
// 	}
// 	executeRes.Output = string(executeOutput)
// 	assert.Equal(t, "114", executeRes.Output, executeRes)

// 	id, ok := res.Results[0].FileIDs["tmp.txt"]
// 	if !ok {
// 		t.Errorf("failed to finish execute: failed to cache file, executeRes: %+v", executeRes)
// 	}

// 	s, f := judgeManger.fs.Get(id)
// 	t.Errorf("s: %+v, f: %+v", s, f)
// }

var compileResCaches map[string]CompileRes
var fileCaches map[string]string

func TestCompile(t *testing.T) {
	p := &model.Problem{DefaultTimeLimit: uint32(time.Second), DefaultSpaceLimit: 104857600}
	ctx := context.Background()
	compileResCaches = make(map[string]CompileRes)

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
			if compileRes.ExitStatus != 0 {
				t.Errorf("failed to finish compile: compileRes.Status != 0, compileRes: %+v", compileRes)
			}

			// _, ok := compileRes.ArtifactFileIDs[conf.ArtifactName]
			// if !ok {
			// 	t.Errorf("failed to finish compile: failed to cache fille, compileRes: %+v", compileRes)
			// }
			compileResCaches[language] = *compileRes
			fileCaches[language] = compileRes.ArtifactFileId
		})
	}
}

func TestExecuteFile(t *testing.T) {
	p := &model.Problem{DefaultTimeLimit: uint32(time.Second), DefaultSpaceLimit: 104857600}
	ctx := context.Background()
	stdin := "Hello,world.\n" // should not contain space
	stdout := stdin

	for language, _ := range languageConfig {
		// just for C
		if language != "c" {
			continue
		}

		name, _ := judgeManger.fs.Get(fileCaches[language])

		t.Run(language, func(t *testing.T) {
			executeRes, err := judgeManger.ExecuteFile(ctx, name,
				fileCaches[language],
				&worker.MemoryFile{Content: []byte(stdin)},
				time.Duration(p.DefaultTimeLimit), runner.Size(p.DefaultSpaceLimit),
			)
			assert.NoError(t, err)
			if executeRes.ExitStatus != 0 {
				t.Errorf("failed to execute: executeRes.ExitStatus != 0, executeRes: %+v", executeRes)
			}
			assert.Equal(t, stdout, executeRes.Output, fmt.Sprintf("compileRes: %+v, executeRes: %+v", compileResCaches[language], executeRes))
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
