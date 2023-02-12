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

	"github.com/criyle/go-judge/envexec"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

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
	judgeConf := lo.Must(config.LoadConf("../conf/signal.toml", "tests/language.toml"))
	languageConfig = judgeConf.LanguageConf
	judgeManger = NewJudgeManager(goJudgeWorker, judgeConf, fs, logger)

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

var fileCaches map[string]map[string]string

func TestCompile(t *testing.T) {
	ctx := context.Background()
	fileCaches = make(map[string]map[string]string)

	for language, conf := range languageConfig {
		t.Run(language, func(t *testing.T) {
			codes, err := os.ReadFile("tests/" + conf.SourceName)
			assert.NoError(t, err)

			sub := &model.Submission{Language: language, SourceCode: codes}
			compileRes, err := judgeManger.Compile(ctx, sub)
			assert.NotNil(t, compileRes)
			defer func() {
				if compileRes.Stdout != nil {
					_ = compileRes.Stdout.Close()
				}
				if compileRes.Stderr != nil {
					_ = compileRes.Stderr.Close()

				}
			}()
			assert.NoError(t, err)

			if compileRes.Status != envexec.StatusAccepted {
				stdout, _ := io.ReadAll(compileRes.Stdout)
				stderr, _ := io.ReadAll(compileRes.Stderr)
				t.Errorf("failed to finish compile: compileRes.Status != 0, compile output: \n%s, compileRes: \n%+v",
					"==stdout==\n"+string(stdout)+"==stderr==\n"+string(stderr), compileRes)
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
		t.Run(language, func(t *testing.T) {
			executeRes, err := judgeManger.Execute(ctx,
				conf.ExecuteCmd,
				&worker.MemoryFile{Content: []byte(stdin)}, fileCaches[language],
				time.Duration(p.DefaultTimeLimit), runner.Size(p.DefaultSpaceLimit),
			)
			defer func() {
				_ = executeRes.Stdout.Close()
				_ = executeRes.Stderr.Close()
			}()

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
		t.Run(language, func(t *testing.T) {
			judgeRes, err := judgeManger.Judge(
				ctx, nil, language, fileCaches[language], testcase,
				time.Duration(p.DefaultTimeLimit), runner.Size(p.DefaultSpaceLimit),
			)
			assert.NoError(t, err)
			assert.Equal(t, model.Conclusion_Accepted, judgeRes.Conclusion)

			buff, err := io.ReadAll(judgeRes.Stdout)
			assert.NoError(t, err)
			assert.Equal(t, "Hello,world.\n", string(buff))
		})
	}
}

func TestCompileOption(t *testing.T) {
	ctx := context.Background()
	stdin := "Hello,world.\n" // should not contain space
	stdout := stdin

	for language, conf := range languageConfig {
		for optName := range conf.Options {
			t.Run(language, func(t *testing.T) {
				codes, err := os.ReadFile("tests/" + optName + "_" + conf.ArtifactName)
				if os.IsNotExist(err) {
					// use normal codes
					codes, err = os.ReadFile("tests/" + conf.SourceName)
					assert.NoError(t, err)
				}
				sub := &model.Submission{Language: language, SourceCode: codes, CompilerOption: `["` + optName + `"]`}

				compileRes, err := judgeManger.Compile(ctx, sub)
				assert.NotNil(t, compileRes, "")
				defer func() {
					if compileRes.Stdout != nil {
						_ = compileRes.Stdout.Close()
					}
					if compileRes.Stderr != nil {
						_ = compileRes.Stderr.Close()

					}
				}()
				assert.NoError(t, err)

				if compileRes.Status != envexec.StatusAccepted {
					stdout, _ := io.ReadAll(compileRes.Stdout)
					stderr, _ := io.ReadAll(compileRes.Stderr)
					t.Errorf("failed to finish compile: compileRes.Status != 0, compile output: \n%s, compileRes: \n%+v",
						"==stdout==\n"+string(stdout)+"==stderr==\n"+string(stderr), compileRes)
				}

				executeRes, err := judgeManger.Execute(ctx,
					conf.ExecuteCmd,
					&worker.MemoryFile{Content: []byte(stdin)}, compileRes.ArtifactFileIDs,
					time.Second, runner.Size(104857600),
				)
				defer func() {
					_ = executeRes.Stdout.Close()
					_ = executeRes.Stderr.Close()
				}()

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
}

func TestCompileMultiOptions(t *testing.T) {
	// just for cpp
	language := "cpp"
	conf := languageConfig["cpp"]
	ctx := context.Background()

	codes, err := os.ReadFile("tests/source_cpp14.cpp")
	assert.NoError(t, err)

	sub := &model.Submission{Language: language, SourceCode: codes, CompilerOption: `["cpp14", "o2"]`}
	compileRes, err := judgeManger.Compile(ctx, sub)
	assert.NotNil(t, compileRes)
	defer func() {
		if compileRes.Stdout != nil {
			_ = compileRes.Stdout.Close()
		}
		if compileRes.Stderr != nil {
			_ = compileRes.Stderr.Close()

		}
	}()
	assert.NoError(t, err)

	if compileRes.Status != envexec.StatusAccepted {
		stdout, _ := io.ReadAll(compileRes.Stdout)
		stderr, _ := io.ReadAll(compileRes.Stderr)
		t.Errorf("failed to finish compile: compileRes.Status != 0, compile output: \n%s, compileRes: \n%+v",
			"==stdout==\n"+string(stdout)+"==stderr==\n"+string(stderr), compileRes)
	}

	executeRes, err := judgeManger.Execute(ctx,
		conf.ExecuteCmd,
		&worker.MemoryFile{Content: []byte("")}, compileRes.ArtifactFileIDs,
		time.Second, runner.Size(104857600),
	)
	defer func() {
		_ = executeRes.Stdout.Close()
		_ = executeRes.Stderr.Close()
	}()

	assert.NoError(t, err)
	if executeRes.ExitStatus != 0 {
		t.Errorf("failed to execute: executeRes.ExitStatus != 0, executeRes: %+v", executeRes)
	}
	bytes, err := io.ReadAll(executeRes.Stdout)
	if err != nil {
		t.Errorf("failed to read execute output, executeRes: %+v", executeRes)
	}

	assert.Equal(t, "8888889885\n", string(bytes), fmt.Sprintf("executeRes: %+v", executeRes))
}
