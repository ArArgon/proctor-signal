package judge

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/samber/lo"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/yaml.v3"

	judgeconfig "github.com/criyle/go-judge/cmd/executorserver/config"
	"github.com/criyle/go-judge/worker"
	"github.com/stretchr/testify/assert"

	"proctor-signal/config"
	"proctor-signal/external/gojudge"
	"proctor-signal/model"
	"proctor-signal/resource"

	"github.com/criyle/go-sandbox/runner"
)

var judgeManger *Manager

var languageConfig config.LanguageConf

func TestMain(m *testing.M) {
	// Init gojudge
	initGoJudge()

	// Init logger.
	zapConfig := zap.NewDevelopmentConfig()
	zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	zapConfig.Level.SetLevel(zap.InfoLevel)
	logger := lo.Must(zapConfig.Build())

	conf := loadConf()
	gojudge.Init(logger, conf)

	// Prepare tmp dir.
	loc, err := os.MkdirTemp(os.TempDir(), "signal")
	if err != nil {
		panic("faild to prepare tmp dir: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(loc) }()

	// Init fs.
	fs := lo.Must(resource.NewFileStore(logger, loc))

	b := gojudge.NewEnvBuilder(conf)
	envPool := gojudge.NewEnvPool(b, false)
	gojudge.Prefork(envPool, conf.PreFork)
	goJudgeWorker := gojudge.NewWorker(conf, envPool, fs)

	// init judgeManger
	languageConfig = loadLanguageConfig("tests/language.yaml")
	judgeManger = NewJudgeManager(goJudgeWorker, languageConfig)
	judgeManger.fs = fs

	os.Exit(m.Run())
}

func loadConf() *config.JudgeConfig {
	var conf judgeconfig.Config
	if err := conf.Load(); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		log.Fatalln("load config failed ", err)
	}
	return (*config.JudgeConfig)(&conf)
}

func loadLanguageConfig(configPath string) config.LanguageConf {
	f, err := os.Open(configPath)
	defer func() { _ = f.Close() }()

	if err != nil {
		fmt.Printf("err: %v\n", err)
		panic("fail to open language config")
	}
	res := make(config.LanguageConf)
	err = yaml.NewDecoder(f).Decode(&res)
	if err != nil {
		fmt.Printf("err: %v\n", err)
		panic("fail to decode language config")
	}
	return res
}

// func TestWokerExecute(t *testing.T) {
// 	ctx := context.Background()

// 	res := <-judgeManger.worker.Execute(ctx, &worker.Request{
// 		Cmd: []worker.Cmd{{
// 			Env:         []string{"PATH=/usr/bin:/bin"},
// 			Args:        []string{"echo", "114514"},
// 			CPULimit:    time.Second,
// 			MemoryLimit: 104857600,
// 			ProcLimit:   50,
// 			Files: []worker.CmdFile{
// 				&worker.MemoryFile{Content: []byte("")},
// 				&worker.Collector{Name: "stdout", Max: 10240},
// 				&worker.Collector{Name: "stderr", Max: 10240},
// 			},
// 			CopyOut: []worker.CmdCopyOutFile{
// 				{Name: "stdout", Optional: true},
// 				{Name: "stderr", Optional: true},
// 			},
// 			CopyOutCached: []worker.CmdCopyOutFile{
// 				{Name: "stdout", Optional: true},
// 			},
// 		}},
// 	})

// 	// executeRes := &ExecuteRes{
// 	// 	Status:     res.Results[0].Status,
// 	// 	ExitStatus: res.Results[0].ExitStatus,
// 	// 	Error:      res.Results[0].Error,
// 	// }
// 	assert.NoError(t, res.Error)

// 	var executeOutput []byte
// 	var err error
// 	_, err = res.Results[0].Files["stdout"].Seek(0, 0)
// 	assert.NoError(t, err, "failed to reseek execute stdout: ")
// 	executeOutput, err = io.ReadAll(res.Results[0].Files["stdout"])
// 	assert.NoError(t, err, "failed to read execute stdout: ")

// 	s, f := judgeManger.fs.Get(res.Results[0].FileIDs["stdout"])
// 	assert.Equal(t, s, "stdout")
// 	r, err := envexec.FileToReader(f)
// 	assert.NoError(t, err, "failed to cached execute stdout: ")

// 	bs, err := io.ReadAll(r)
// 	assert.NoError(t, err, "failed to read cached execute stdout: ")
// 	assert.Equal(t, executeOutput, bs, string(executeOutput)+"!="+string(bs))

// 	// // read execute output
// 	// var executeOutput []byte
// 	// var err error
// 	// if res.Results[0].ExitStatus == 0 {
// 	// 	_, err = res.Results[0].Files["stdout"].Seek(0, 0)
// 	// 	assert.NoError(t, err, "failed to reseek execute stdout: ")

// 	// 	executeOutput, err = io.ReadAll(res.Results[0].Files["stdout"])
// 	// 	assert.NoError(t, err, "failed to read execute stdout: ")
// 	// } else {
// 	// 	_, err = res.Results[0].Files["stderr"].Seek(0, 0)
// 	// 	assert.NoError(t, err, "failed to reseek execute stderr: ")

// 	// 	executeOutput, err = io.ReadAll(res.Results[0].Files["stderr"])
// 	// 	assert.NoError(t, err, "failed to read execute stderr: ")
// 	// }
// 	// executeRes.Output = string(executeOutput)
// 	// assert.Equal(t, "114", executeRes.Output, executeRes)

// 	// id, ok := res.Results[0].FileIDs["tmp.txt"]
// 	// if !ok {
// 	// 	t.Errorf("failed to finish execute: failed to cache file, executeRes: %+v", executeRes)
// 	// }

// 	// s, f := judgeManger.fs.Get(id)
// 	// t.Errorf("s: %+v, f: %+v", s, f)
// }

var fileCaches map[string]map[string]string

func TestCompile(t *testing.T) {
	// p := &model.Problem{DefaultTimeLimit: uint32(time.Second), DefaultSpaceLimit: 104857600}
	ctx := context.Background()
	fileCaches = make(map[string]map[string]string)

	for language, conf := range languageConfig {
		// just for C
		if language != "c" {
			continue
		}

		t.Run(language, func(t *testing.T) {
			codes, err := os.ReadFile("tests/" + conf.SourceName)
			assert.NoError(t, err)
			sub := &model.Submission{Language: language, SourceCode: codes}

			compileRes, err := judgeManger.Compile(ctx, sub)
			assert.NotNil(t, compileRes)
			assert.NoError(t, err)
			if compileRes.ExitStatus != 0 {
				t.Errorf("failed to finish compile: compileRes.Status != 0, compileRes: %+v", compileRes)
			}
			fileCaches[language] = compileRes.ArtifactFileIDs
		})
	}
}

func TestExecute(t *testing.T) {
	p := &model.Problem{DefaultTimeLimit: uint32(time.Second), DefaultSpaceLimit: 104857600}
	ctx := context.Background()
	stdin := "Hello,world.\n" // should not contain space
	stdout := stdin

	for language, conf := range languageConfig {
		// just for C
		if language != "c" {
			continue
		}

		t.Run(language, func(t *testing.T) {
			executeRes, err := judgeManger.Execute(ctx,
				conf.ExecuteCmd,
				&worker.MemoryFile{Content: []byte(stdin)}, fileCaches[language],
				false, time.Duration(p.DefaultTimeLimit), runner.Size(p.DefaultSpaceLimit),
			)
			assert.NoError(t, err)
			if executeRes.ExitStatus != 0 {
				t.Errorf("failed to execute: executeRes.ExitStatus != 0, executeRes: %+v", executeRes)
			}
			bytes, err := io.ReadAll(executeRes.Stdout)
			if err != nil {
				t.Errorf("failed to read execute output, executeRes: %+v", executeRes)
			}

			assert.Equal(t, stdout, string(bytes), fmt.Sprintf("executeRes: %+v", executeRes))
		})
	}
}

func TestJudge(t *testing.T) {
	p := &model.Problem{DefaultTimeLimit: uint32(time.Second), DefaultSpaceLimit: 104857600}
	ctx := context.Background()

	var err error
	testcase := &model.TestCase{}
	f, err := judgeManger.fs.New()
	assert.NoError(t, err)
	_, err = io.WriteString(f, "Hello,world.\n")
	assert.NoError(t, err)
	testcase.InputKey, err = judgeManger.fs.Add("stddin", f.Name())
	assert.NoError(t, err)
	testcase.OutputKey = testcase.InputKey

	for language := range languageConfig {
		// just for C
		if language != "c" {
			continue
		}

		t.Run(language, func(t *testing.T) {
			judgeRes, err := judgeManger.Judge(ctx, language, fileCaches[language], testcase, time.Duration(p.DefaultTimeLimit), runner.Size(p.DefaultSpaceLimit))
			assert.NoError(t, err)
			assert.Equal(t, model.Conclusion_Accepted, judgeRes.Conclusion)

			bs, err := io.ReadAll(judgeRes.Output)
			assert.NoError(t, err)
			assert.Equal(t, "Hello,world.\n", string(bs))
		})
	}
}
