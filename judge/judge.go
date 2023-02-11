package judge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/samber/lo"
	"go.uber.org/zap"

	"proctor-signal/config"
	"proctor-signal/model"
	"proctor-signal/resource"

	"github.com/criyle/go-judge/envexec"
	"github.com/criyle/go-judge/worker"
	"github.com/criyle/go-sandbox/runner"
)

const maxStdoutSize = 50 * 1024 * 1024        // 50 MiB
const maxCompilerStderrSize = 1 * 1024 * 1024 // 1 MiB

func NewJudgeManager(
	worker worker.Worker, conf *config.Config, fs *resource.FileStore, logger *zap.Logger,
) (*Manager, error) {
	lang, err := newLanguageConfigs(conf.LanguageConf, conf.JudgeOptions.Environment)
	if err != nil {
		return nil, err
	}
	return &Manager{
		fs:     fs,
		worker: worker,
		conf:   conf,
		lang:   lang,
		logger: logger,
	}, nil
}

type JudgeOptions struct {
	MaxTruncatedOutput uint `default:"10240"`
}

type Manager struct {
	worker worker.Worker
	fs     *resource.FileStore // share with worker
	conf   *config.Config
	lang   map[string]*languageConf
	logger *zap.Logger
}

type ExecuteRes struct {
	Status     envexec.Status
	ExitStatus int
	Error      string
	Stdout     io.ReadSeekCloser
	StdoutSize int64
	Stderr     io.ReadSeekCloser
	StderrSize int64
	TotalTime  time.Duration
	TotalSpace runner.Size
}

type CompileRes struct {
	ExecuteRes
	ArtifactFileIDs map[string]string
}

type JudgeRes struct {
	ExitStatus      int
	Error           string
	Conclusion      model.Conclusion
	OutputID        string
	OutputSize      int64
	TruncatedOutput string
	TotalTime       time.Duration
	TotalSpace      runner.Size
}

func (m *Manager) RemoveFiles(fileIDs map[string]string) {
	if fileIDs == nil {
		return
	}
	_, err := m.fs.BulkRemove(lo.Values(fileIDs))
	if err != nil {
		m.logger.Sugar().With("err", err).Error("failed to remove files: %+v", fileIDs)
	}
}

func (m *Manager) Compile(ctx context.Context, sub *model.Submission) (*CompileRes, error) {
	compileConf, ok := m.lang[sub.Language]
	if !ok {
		return nil, fmt.Errorf("compile config for %s not found", sub.Language)
	}

	args, err := compileConf.evalCompileCmd(sub)
	if err != nil {
		return nil, fmt.Errorf("failed to compose compilation Cmd, err: %+v", err)
	}

	res := <-m.worker.Execute(ctx, &worker.Request{
		Cmd: []worker.Cmd{{
			Env:         compileConf.getEnvs(),
			Args:        args,
			CPULimit:    time.Duration(compileConf.raw.CompileTimeLimit) * time.Millisecond,
			MemoryLimit: runner.Size(compileConf.raw.CompileSpaceLimit),
			ProcLimit:   50,
			Files: []worker.CmdFile{
				&worker.MemoryFile{Content: []byte("")},
				&worker.Collector{Name: "stdout", Max: maxCompilerStderrSize},
				&worker.Collector{Name: "stderr", Max: maxCompilerStderrSize},
			},
			CopyIn: map[string]worker.CmdFile{
				compileConf.getSourceName(): &worker.MemoryFile{Content: sub.SourceCode},
			},
			CopyOutCached: []worker.CmdCopyOutFile{
				{Name: compileConf.getArtifactName(), Optional: true},
			},
			CopyOut: []worker.CmdCopyOutFile{
				{Name: "stdout"},
				{Name: "stderr"},
			},
		}},
	})
	result := &res.Results[0]

	compileRes := &CompileRes{
		ExecuteRes: ExecuteRes{
			Status:     result.Status,
			ExitStatus: result.ExitStatus,
			Error:      result.Error,
			TotalTime:  result.RunTime,
			TotalSpace: result.Memory,
		},
	}

	if res.Error != nil {
		return compileRes, res.Error
	}

	// read compile output
	var f *os.File
	f, ok = result.Files["stdout"]
	if ok {
		compileRes.Stdout = f
		if _, err = f.Seek(0, 0); err != nil {
			return compileRes, errors.New("failed to reseek compile stdout")
		}
	}
	f, ok = result.Files["stderr"]
	if ok {
		compileRes.Stderr = f
		if _, err = f.Seek(0, 0); err != nil {
			return compileRes, errors.New("failed to reseek compile stderr")
		}
	}

	// cache files
	compileRes.ArtifactFileIDs = result.FileIDs
	if _, ok = result.FileIDs[compileConf.getArtifactName()]; !ok && result.Status == envexec.StatusAccepted {
		return compileRes, errors.New("failed to cache ArtifactFile")
	}

	return compileRes, nil
}

// Execute executes cmd with stdin and copyIn files.
func (m *Manager) Execute(ctx context.Context, cmd string, stdin worker.CmdFile, copyInFileIDs map[string]string, CPULimit time.Duration, memoryLimit runner.Size) (*ExecuteRes, error) {
	workerCmd := worker.Cmd{
		Env:         []string{"PATH=/usr/bin:/bin"},
		Args:        strings.Split(cmd, " "),
		CPULimit:    CPULimit,
		ClockLimit:  CPULimit * 2,
		MemoryLimit: memoryLimit,
		ProcLimit:   50,
		Files: []worker.CmdFile{
			stdin,
			&worker.Collector{Name: "stdout", Max: maxStdoutSize},
			&worker.Collector{Name: "stderr", Max: maxStdoutSize},
		},
		CopyOut: []worker.CmdCopyOutFile{
			{Name: "stdout", Optional: true},
			{Name: "stderr", Optional: true},
		},
	}
	workerCmd.CopyIn = make(map[string]worker.CmdFile)
	for filename, fileID := range copyInFileIDs {
		workerCmd.CopyIn[filename] = &worker.CachedFile{FileID: fileID}
	}

	res := <-m.worker.Execute(ctx, &worker.Request{Cmd: []worker.Cmd{workerCmd}})
	if res.Error != nil {
		return nil, res.Error
	}

	result := &res.Results[0]
	executeRes := &ExecuteRes{
		Status:     result.Status,
		ExitStatus: result.ExitStatus,
		Error:      result.Error,
		TotalTime:  result.RunTime,
		TotalSpace: result.Memory,
	}

	// read execute output
	var err error
	if f, ok := result.Files["stdout"]; ok {
		if _, err = f.Seek(0, 0); err != nil {
			return executeRes, errors.New("failed to reseek execute stdout")
		}
		executeRes.Stdout = f

		fi, err := f.Stat()
		if err != nil {
			return executeRes, errors.New("failed to read execute stdout info")
		}
		executeRes.StdoutSize = fi.Size()
	}

	if f, ok := result.Files["stderr"]; ok {
		if _, err = f.Seek(0, 0); err != nil {
			return executeRes, errors.New("failed to reseek execute stderr")
		}
		executeRes.Stderr = f

		fi, err := f.Stat()
		if err != nil {
			return executeRes, errors.New("failed to read execute stderr info")
		}
		executeRes.StderrSize = fi.Size()
	}

	return executeRes, nil
}

func fsKey(p *model.Problem, key string) string {
	binFileKey := key
	if p != nil {
		binFileKey = resource.ResKey(p, binFileKey)
	}

	return binFileKey
}

func (m *Manager) Judge(
	ctx context.Context, p *model.Problem, language string, copyInFileIDs map[string]string,
	testcase *model.TestCase, CPULimit time.Duration, memoryLimit runner.Size,
) (*JudgeRes, error) {
	conf, ok := m.lang[language]
	if !ok {
		return nil, fmt.Errorf("config for %s not found", language)
	}

	executeRes, err := m.Execute(
		ctx, strings.Join(conf.getRunCmd(), " "), &worker.CachedFile{FileID: fsKey(p, testcase.InputKey)},
		copyInFileIDs, CPULimit, memoryLimit,
	)
	if executeRes == nil {
		return nil, err
	}

	defer func() {
		_ = executeRes.Stdout.Close()
		_ = executeRes.Stderr.Close()
	}()

	judgeRes := &JudgeRes{
		ExitStatus: executeRes.ExitStatus,
		Error:      executeRes.Error,
		TotalTime:  executeRes.TotalTime,
		TotalSpace: executeRes.TotalSpace,
		Conclusion: model.ConvertStatusToConclusion(executeRes.Status),
	}
	if err != nil {
		return judgeRes, err
	}

	if executeRes.Status != envexec.StatusAccepted {
		return judgeRes, nil
	}

	_, ef := m.fs.Get(fsKey(p, testcase.OutputKey))
	testcaseOutputReader, err := envexec.FileToReader(ef)
	if err != nil {
		judgeRes.Conclusion = model.Conclusion_InternalError
		return judgeRes, err
	}

	ok, err = compare(testcaseOutputReader, executeRes.Stdout, 1024) // Only judge on executeRes.Stdout, ignore executeRes.Stderr
	if err != nil {
		judgeRes.Conclusion = model.Conclusion_InternalError
		return judgeRes, err
	}

	judgeRes.Conclusion = lo.Ternary(ok, model.Conclusion_Accepted, model.Conclusion_WrongAnswer)

	// reset executeRes.Stdout
	if _, err = executeRes.Stdout.Seek(0, 0); err != nil {
		return judgeRes, err
	}

	// read judge output
	buffLen := lo.Clamp(executeRes.StdoutSize, 0, int64(m.conf.JudgeOptions.MaxTruncatedOutput))
	buff := make([]byte, buffLen)
	judgeRes.OutputSize = executeRes.StdoutSize

	if _, err = io.ReadFull(executeRes.Stdout, buff); err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return judgeRes, err
	}
	judgeRes.TruncatedOutput = string(buff)

	// Cache executeRes.Stdout as judge output
	f, ok := executeRes.Stdout.(*os.File)
	if ok {
		judgeRes.OutputID, err = m.fs.Add("Stdout", f.Name())
		if err != nil {
			return judgeRes, err
		}
	}

	return judgeRes, nil
}

// Compare compares actual content to expected content, return the result whether they are equal.
// It should just be called by func Judge.
func compare(expected, actual io.Reader, buffLen int) (bool, error) {
	expectedOutputBuff := make([]byte, buffLen)
	actualOutputBuff := make([]byte, buffLen)
	var expectedLen, actualLen int
	var err error
	for {
		expectedLen, err = io.ReadFull(expected, expectedOutputBuff)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return false, err
		}

		actualLen, err = io.ReadFull(actual, actualOutputBuff)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return false, err
		}

		if actualLen == expectedLen+1 {
			if actualOutputBuff[actualLen-1] == ' ' || actualOutputBuff[actualLen-1] == '\n' {
				// cut off ' ' or '\n' at the end of executeOutputBuff
				actualLen--
			} else {
				return false, nil
			}
		} else if actualLen > expectedLen+1 || actualLen < expectedLen {
			return false, nil
		}

		if !reflect.DeepEqual(expectedOutputBuff[:expectedLen], actualOutputBuff[:actualLen]) {
			return false, nil
		}

		if expectedLen < buffLen {
			break
		}
	}

	return true, nil
}
