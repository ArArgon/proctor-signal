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

var (
	judgeManger    *Manager
	languageConfig config.LanguageConf
	problem        *model.Problem
	testcase       *model.TestCase
)

func TestMain(m *testing.M) {
	// Init gojudge
	initGoJudge()

	// Init logger.
	zapConfig := zap.NewDevelopmentConfig()
	zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	zapConfig.Level.SetLevel(zap.InfoLevel)
	logger := lo.Must(zapConfig.Build())

	conf := loadConf()
	gojudge.Init(logger)

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
	judgeManger = lo.Must(NewJudgeManager(goJudgeWorker, judgeConf, fs, logger))

	// prepare problem
	problem = &model.Problem{
		DefaultTimeLimit:  uint32(time.Second),
		DefaultSpaceLimit: 256,
		DiffPolicy:        model.DiffPolicy_LINE,
		IgnoreNewline:     true,
	}

	// prepare testcase
	stdin := "Hello,world.\n" // should not contain space
	testcase = &model.TestCase{}
	f, err := judgeManger.fs.New()
	if err != nil {
		panic("failed to new file from fs, err: " + err.Error())
	}
	if _, err = io.WriteString(f, stdin); err != nil {
		panic("failed to write into file, err: " + err.Error())
	}

	testcase.InputKey, err = judgeManger.fs.Add("stdin", f.Name())
	if err != nil {
		panic("failed to add file into fs, err: " + err.Error())
	}
	testcase.OutputKey = testcase.InputKey

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

func getTestcaseOutput(testcase *model.TestCase) ([]byte, error) {
	f, _, err := judgeManger.fs.GetOsFile(testcase.OutputKey)
	if err != nil {
		return nil, err
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return data, nil
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

	for language, conf := range languageConfig {
		t.Run(language, func(t *testing.T) {
			executeRes, err := judgeManger.Execute(ctx,
				conf.ExecuteCmd,
				&worker.CachedFile{FileID: testcase.InputKey}, fileCaches[language], "",
				time.Duration(p.DefaultTimeLimit), runner.Size(p.DefaultSpaceLimit),
			)
			assert.NotNil(t, executeRes)
			assert.NoError(t, err)
			if executeRes.ExitStatus != 0 {
				t.Errorf("failed to execute: executeRes.ExitStatus != 0, executeRes: %+v", executeRes)
			}

			testcaseOutput, err := getTestcaseOutput(testcase)
			assert.NoErrorf(t, err, "failed to read tesetcase output")

			executeOutput, err := io.ReadAll(executeRes.Stdout)
			if err != nil {
				t.Errorf("failed to read execute output, executeRes: %+v", executeRes)
			}

			assert.Equal(t, testcaseOutput, executeOutput, fmt.Sprintf("executeRes: %+v", executeRes))
		})
	}
}

func TestJudge(t *testing.T) {
	ctx := context.Background()

	for language := range languageConfig {
		t.Run(language, func(t *testing.T) {
			judgeRes, err := judgeManger.Judge(ctx, problem, language, fileCaches[language], testcase)
			assert.NotNil(t, judgeRes)
			assert.NoError(t, err)
			assert.Equal(t, model.Conclusion_Accepted, judgeRes.Conclusion)

			// recheck
			testcaseOutput, err := getTestcaseOutput(testcase)
			assert.NoErrorf(t, err, "failed to read tesetcase output")

			judgeOutput, err := io.ReadAll(judgeRes.Stdout)
			if err != nil {
				t.Errorf("failed to read judge output, judgeRes: %+v", judgeRes)
			}

			assert.Equal(t, testcaseOutput, judgeOutput)
		})
	}
}

func TestCompileOption(t *testing.T) {
	ctx := context.Background()

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
				assert.NoError(t, err)

				if compileRes.Status != envexec.StatusAccepted {
					stdout, _ := io.ReadAll(compileRes.Stdout)
					stderr, _ := io.ReadAll(compileRes.Stderr)
					t.Errorf("failed to finish compile: compileRes.Status != 0, compile output: \n%s, compileRes: \n%+v",
						"==stdout==\n"+string(stdout)+"==stderr==\n"+string(stderr), compileRes)
				}

				judgeRes, err := judgeManger.Judge(ctx, problem, language, fileCaches[language], testcase)
				assert.NotNil(t, judgeRes)
				assert.NoError(t, err)
				assert.Equal(t, model.Conclusion_Accepted, judgeRes.Conclusion)

				// recheck
				testcaseOutput, err := getTestcaseOutput(testcase)
				assert.NoErrorf(t, err, "failed to read tesetcase output")

				judgeOutput, err := io.ReadAll(judgeRes.Stdout)
				if err != nil {
					t.Errorf("failed to read judge output, judgeRes: %+v", judgeRes)
				}

				assert.Equal(t, testcaseOutput, judgeOutput)
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
	assert.NoError(t, err)

	if compileRes.Status != envexec.StatusAccepted {
		stdout, _ := io.ReadAll(compileRes.Stdout)
		stderr, _ := io.ReadAll(compileRes.Stderr)
		t.Fatalf("failed to finish compile: compileRes.Status != 0, compile output: \n%s, compileRes: \n%+v",
			"==stdout==\n"+string(stdout)+"==stderr==\n"+string(stderr), compileRes)
	}

	executeRes, err := judgeManger.Execute(ctx,
		conf.ExecuteCmd,
		&worker.MemoryFile{Content: []byte("")}, compileRes.ArtifactFileIDs, "",
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

func TestNoCompilation(t *testing.T) {
	language := "python"
	conf := languageConfig[language]
	ctx := context.Background()

	codes, err := os.ReadFile("tests/source.py")
	assert.NoError(t, err)

	sub := &model.Submission{Language: language, SourceCode: codes}
	compileRes, err := judgeManger.Compile(ctx, sub)
	assert.NoError(t, err)
	assert.NotNil(t, compileRes)
	assert.True(t, compileRes.ArtifactFileIDs[conf.SourceName] != "")
	assert.Equal(t, compileRes.Status, envexec.StatusAccepted)
}

func TestJudgeByFiles(t *testing.T) {
	ctx := context.Background()
	language := "c"
	codes, err := os.ReadFile("tests/source_from_files.c")
	assert.NoError(t, err)

	sub := &model.Submission{Language: language, SourceCode: codes}
	compileRes, err := judgeManger.Compile(ctx, sub)
	assert.NotNil(t, compileRes)
	assert.NoError(t, err)

	if compileRes.Status != envexec.StatusAccepted {
		stdout, _ := io.ReadAll(compileRes.Stdout)
		stderr, _ := io.ReadAll(compileRes.Stderr)
		t.Fatalf("failed to finish compile: compileRes.Status != 0, compile output: \n%s, compileRes: \n%+v",
			"==stdout==\n"+string(stdout)+"==stderr==\n"+string(stderr), compileRes)
	}

	t.Run("file-read-and-write", func(t *testing.T) {
		p := &model.Problem{
			DefaultTimeLimit:  uint32(time.Second),
			DefaultSpaceLimit: 256,
			DiffPolicy:        model.DiffPolicy_LINE,
			IgnoreNewline:     true,
			InputFile:         "input",
			OutputFile:        "output",
		}
		copyInFileIDs := lo.Assign(compileRes.ArtifactFileIDs)

		judgeRes, err := judgeManger.Judge(ctx, p, language, copyInFileIDs, testcase)
		assert.NotNil(t, judgeRes)
		assert.NoError(t, err)

		assert.Equal(t, model.Conclusion_Accepted, judgeRes.Conclusion)
	})

	t.Run("read-from-stdin", func(t *testing.T) {
		p := &model.Problem{
			DefaultTimeLimit:  uint32(time.Second),
			DefaultSpaceLimit: 256,
			DiffPolicy:        model.DiffPolicy_LINE,
			IgnoreNewline:     true,
			OutputFile:        "output",
		}
		copyInFileIDs := lo.Assign(compileRes.ArtifactFileIDs)

		judgeRes, err := judgeManger.Judge(ctx, p, language, copyInFileIDs, testcase)
		assert.NotNil(t, judgeRes)
		assert.NoError(t, err)

		assert.NotEqual(t, model.Conclusion_InternalError, judgeRes.Conclusion)
		assert.NotEqual(t, model.Conclusion_Accepted, judgeRes.Conclusion)
	})

	t.Run("write-to-stdout", func(t *testing.T) {
		p := &model.Problem{
			DefaultTimeLimit:  uint32(time.Second),
			DefaultSpaceLimit: 256,
			DiffPolicy:        model.DiffPolicy_LINE,
			IgnoreNewline:     true,
			InputFile:         "input",
		}
		copyInFileIDs := lo.Assign(compileRes.ArtifactFileIDs)

		judgeRes, err := judgeManger.Judge(ctx, p, language, copyInFileIDs, testcase)
		assert.NotNil(t, judgeRes)
		assert.NoError(t, err)

		assert.Equal(t, model.Conclusion_WrongAnswer, judgeRes.Conclusion)
		assert.EqualValues(t, 0, judgeRes.OutputSize)
	})
}

func TestJudgeByFiles2(t *testing.T) {
	ctx := context.Background()
	language := "c"
	// tests/source.c reads from stdin and outputs to stdout.
	codes, err := os.ReadFile("tests/source.c")
	assert.NoError(t, err)

	sub := &model.Submission{Language: language, SourceCode: codes}
	compileRes, err := judgeManger.Compile(ctx, sub)
	assert.NotNil(t, compileRes)
	assert.NoError(t, err)

	if compileRes.Status != envexec.StatusAccepted {
		stdout, _ := io.ReadAll(compileRes.Stdout)
		stderr, _ := io.ReadAll(compileRes.Stderr)
		t.Fatalf("failed to finish compile: compileRes.Status != 0, compile output: \n%s, compileRes: \n%+v",
			"==stdout==\n"+string(stdout)+"==stderr==\n"+string(stderr), compileRes)
	}

	t.Run("file-read-and-write", func(t *testing.T) {
		p := &model.Problem{
			DefaultTimeLimit:  uint32(time.Second),
			DefaultSpaceLimit: 256,
			DiffPolicy:        model.DiffPolicy_LINE,
			IgnoreNewline:     true,
			InputFile:         "input",
			OutputFile:        "output",
		}
		copyInFileIDs := lo.Assign(compileRes.ArtifactFileIDs)

		judgeRes, err := judgeManger.Judge(ctx, p, language, copyInFileIDs, testcase)
		assert.NotNil(t, judgeRes)
		assert.NoError(t, err)

		assert.NotEqual(t, model.Conclusion_InternalError, judgeRes.Conclusion)
		assert.NotEqual(t, model.Conclusion_Accepted, judgeRes.Conclusion)
	})
}
