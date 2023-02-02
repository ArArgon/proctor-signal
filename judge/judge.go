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

	"proctor-signal/config"
	"proctor-signal/model"
	"proctor-signal/resource"

	"github.com/criyle/go-judge/envexec"
	"github.com/criyle/go-judge/worker"
	"github.com/criyle/go-sandbox/runner"
)

func NewJudgeManager(worker worker.Worker, conf *config.Config) *Manager {
	return &Manager{
		worker: worker,
		conf:   conf,
	}
}

type JudgeOptions struct {
	MaxTruncatedOutput uint `default:"10240"`
}

type Manager struct {
	worker worker.Worker
	fs     *resource.FileStore // share with worker
	conf   *config.Config
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

//var languageConfig map[string]struct {
//	SourceName        string            `yaml:"SourceName"`
//	ArtifactName      string            `yaml:"ArtifactName"`
//	CompileCmd        string            `yaml:"CompileCmd"`
//	CompileTimeLimit  uint32            `yaml:"CompileTimeLimit"`
//	CompileSpaceLimit uint64            `yaml:"CompileSpaceLimit"`
//	ExecuteCmd        string            `yaml:"ExecuteCmd"`
//	Options           map[string]string `yaml:"Options"`
//}

func (m *Manager) RemoveFiles(fileIDs map[string]string) {
	for _, v := range fileIDs {
		m.fs.Remove(v)
	}
}

func (m *Manager) Compile(ctx context.Context, sub *model.Submission) (*CompileRes, error) {
	// TODO: compile options
	compileConf, ok := m.conf.LanguageConf[sub.Language]
	if !ok {
		return nil, fmt.Errorf("compile config for %s not found", sub.Language)
	}

	res := <-m.worker.Execute(ctx, &worker.Request{
		Cmd: []worker.Cmd{{
			Env:         []string{"PATH=/usr/bin:/bin", "SourceName=" + compileConf.SourceName, "ArtifactName=" + compileConf.ArtifactName},
			Args:        strings.Split(compileConf.CompileCmd+" "+compileConf.Options[sub.CompilerOption], " "),
			CPULimit:    time.Duration(compileConf.CompileTimeLimit * 1000000),
			MemoryLimit: runner.Size(compileConf.CompileSpaceLimit),
			ProcLimit:   50,
			Files: []worker.CmdFile{
				&worker.MemoryFile{Content: []byte("")},
				&worker.Collector{Name: "stdout", Max: 10240},
				&worker.Collector{Name: "stderr", Max: 10240},
			},
			CopyIn: map[string]worker.CmdFile{
				compileConf.SourceName: &worker.MemoryFile{Content: sub.SourceCode},
			},
			CopyOutCached: []worker.CmdCopyOutFile{
				{Name: compileConf.ArtifactName, Optional: true},
			},
			CopyOut: []worker.CmdCopyOutFile{
				{Name: "stdout", Optional: true},
				{Name: "stderr", Optional: true},
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
	var err error
	var f *os.File
	f, ok = result.Files["stdout"]
	if ok {
		if _, err = f.Seek(0, 0); err != nil {
			return compileRes, errors.New("failed to reseek compile stdout")
		}
		compileRes.Stdout = f
	}
	f, ok = result.Files["stderr"]
	if ok {
		if _, err = f.Seek(0, 0); err != nil {
			return compileRes, errors.New("failed to reseek compile stderr")
		}
		compileRes.Stderr = f
	}

	// cache files
	if _, ok = result.FileIDs[compileConf.ArtifactName]; !ok {
		return compileRes, errors.New("failed to cache ArtifactFile")
	}
	compileRes.ArtifactFileIDs = result.FileIDs

	return compileRes, nil
}

// ExecuteFile execute cmd with stdin and copyIn files.
func (m *Manager) Execute(ctx context.Context, cmd string, stdin worker.CmdFile, copyInFileIDs map[string]string, CPULimit time.Duration, memoryLimit runner.Size) (*ExecuteRes, error) {
	workerCmd := worker.Cmd{
		Env:         []string{"PATH=/usr/bin:/bin"},
		Args:        strings.Split(cmd, " "),
		CPULimit:    CPULimit,
		MemoryLimit: memoryLimit,
		ProcLimit:   50,
		Files: []worker.CmdFile{
			stdin,
			&worker.Collector{Name: "stdout", Max: 10240},
			&worker.Collector{Name: "stderr", Max: 10240},
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
	result := &res.Results[0]
	executeRes := &ExecuteRes{
		Status:     result.Status,
		ExitStatus: result.ExitStatus,
		Error:      result.Error,
		TotalTime:  result.RunTime,
		TotalSpace: result.Memory,
	}
	if res.Error != nil {
		return executeRes, res.Error
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

func (m *Manager) Judge(ctx context.Context, language string, copyInFileIDs map[string]string, testcase *model.TestCase, CPULimit time.Duration, memoryLimit runner.Size) (*JudgeRes, error) {
	conf, ok := m.conf.LanguageConf[language]
	if !ok {
		return nil, fmt.Errorf("config for %s not found", language)
	}

	executeRes, err := m.Execute(ctx, conf.ExecuteCmd, &worker.CachedFile{FileID: testcase.InputKey}, copyInFileIDs, CPULimit, memoryLimit)
	defer func() {
		_ = executeRes.Stdout.Close()
		_ = executeRes.Stderr.Close()
	}()

	judgeRes := &JudgeRes{
		ExitStatus: executeRes.ExitStatus,
		Error:      executeRes.Error,
		TotalTime:  executeRes.TotalTime,
		TotalSpace: executeRes.TotalSpace,
	}
	if err != nil {
		return judgeRes, err
	}

	_, ef := m.fs.Get(testcase.OutputKey)
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

	if ok {
		judgeRes.Conclusion = model.Conclusion_Accepted
	} else {
		judgeRes.Conclusion = model.Conclusion_WrongAnswer
	}

	// reset executeRes.Stdout
	if _, err = executeRes.Stdout.Seek(0, 0); err != nil {
		return judgeRes, err
	}

	// read judge output
	var buff []byte
	if executeRes.StdoutSize > int64(m.conf.JudgeOptions.MaxTruncatedOutput) {
		buff = make([]byte, m.conf.JudgeOptions.MaxTruncatedOutput)
		judgeRes.OutputSize = int64(m.conf.JudgeOptions.MaxTruncatedOutput)
	} else {
		buff = make([]byte, executeRes.StdoutSize)
		judgeRes.OutputSize = executeRes.StdoutSize
	}

	if _, err = io.ReadFull(executeRes.Stdout, buff); err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return judgeRes, err
	}
	judgeRes.TruncatedOutput = string(buff)

	// Cache executeRes.Stdout as judge output
	f, ok := executeRes.Stdout.(*os.File)
	if ok {
		judgeRes.OutputID, err = m.fs.Add("Stdin", f.Name())
		if err != nil {
			return judgeRes, err
		}
	}

	return judgeRes, nil
}

// Compare compares actual's content to expect's content, return the result whether they are equal.
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
