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

func NewJudgeManager(worker worker.Worker, langConf config.LanguageConf) *Manager {
	return &Manager{
		worker:   worker,
		langConf: langConf,
	}
}

type Manager struct {
	worker   worker.Worker
	fs       *resource.FileStore // share with worker
	langConf config.LanguageConf
}

type CompileRes struct {
	Status          envexec.Status
	ExitStatus      int
	Error           string
	Stdout          io.Reader
	Stderr          io.Reader
	TotalTime       time.Duration
	TotalSpace      runner.Size
	ArtifactFileIDs map[string]string
}

type ExecuteRes struct {
	Status         envexec.Status
	ExitStatus     int
	Error          string
	Stdout         io.Reader
	Stderr         io.Reader
	CachedStdoutID string
	TotalTime      time.Duration
	TotalSpace     runner.Size
}

type JudgeRes struct {
	ExitStatus int
	Error      string
	Conclusion model.Conclusion
	Output     io.Reader
	OutputId   string
	OutputSize uint64
	TotalTime  time.Duration
	TotalSpace runner.Size
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
	compileConf, ok := m.langConf[sub.Language]
	if !ok {
		return nil, fmt.Errorf("compile config for %s not found", sub.Language)
	}

	res := <-m.worker.Execute(ctx, &worker.Request{
		Cmd: []worker.Cmd{{
			Env:         []string{"PATH=/usr/bin:/bin", "SourceName=" + compileConf.SourceName, "ArtifactName=" + compileConf.ArtifactName},
			Args:        strings.Split(compileConf.CompileCmd, " "),
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

	compileRes := &CompileRes{
		Status:     res.Results[0].Status,
		ExitStatus: res.Results[0].ExitStatus,
		Error:      res.Results[0].Error,
		TotalTime:  res.Results[0].RunTime,
		TotalSpace: res.Results[0].Memory,
	}

	_, ok = res.Results[0].FileIDs[compileConf.ArtifactName]
	if !ok {
		return compileRes, errors.New("failed to cache ArtifactFile")
	}
	compileRes.ArtifactFileIDs = res.Results[0].FileIDs

	if res.Error != nil {
		return compileRes, res.Error
	}

	// read compile output
	var err error
	var f *os.File
	f, ok = res.Results[0].Files["stdout"]
	if ok {
		_, err = f.Seek(0, 0)
		if err != nil {
			return compileRes, errors.New("failed to reseek compile stdout")
		}
		compileRes.Stdout = f
	}
	f, ok = res.Results[0].Files["stderr"]
	if ok {
		_, err = f.Seek(0, 0)
		if err != nil {
			return compileRes, errors.New("failed to reseek compile stderr")
		}
		compileRes.Stderr = f
	}

	return compileRes, nil
}

// ExecuteFile execute a runnable file with stdin.
func (m *Manager) Execute(ctx context.Context, cmd string, stdin worker.CmdFile, copyInFileIDs map[string]string, cacheOutput bool, CPULimit time.Duration, memoryLimit runner.Size) (*ExecuteRes, error) {
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

	cmdCopyOutFile := []worker.CmdCopyOutFile{{Name: "stdout", Optional: true}, {Name: "stderr", Optional: true}}
	if cacheOutput {
		workerCmd.CopyOutCached = cmdCopyOutFile
	} else {
		workerCmd.CopyOut = cmdCopyOutFile
	}

	res := <-m.worker.Execute(ctx, &worker.Request{Cmd: []worker.Cmd{workerCmd}})
	executeRes := &ExecuteRes{
		Status:     res.Results[0].Status,
		ExitStatus: res.Results[0].ExitStatus,
		Error:      res.Results[0].Error,
		TotalTime:  res.Results[0].RunTime,
		TotalSpace: res.Results[0].Memory,
	}
	if res.Error != nil {
		return executeRes, res.Error
	}

	// read execute output
	var err error
	var f *os.File
	var ok bool
	if cacheOutput {
		executeRes.CachedStdoutID, ok = res.Results[0].FileIDs["stdout"]
		if !ok {
			return executeRes, errors.New("failed to read execute stdout: " + err.Error())
		}
	} else {
		f, ok = res.Results[0].Files["stdout"]
		if ok {
			_, err = f.Seek(0, 0)
			if err != nil {
				return executeRes, errors.New("failed to reseek execute stdout")
			}
			executeRes.Stdout = f
		}
	}

	f, ok = res.Results[0].Files["stderr"]
	if ok {
		_, err = f.Seek(0, 0)
		if err != nil {
			return executeRes, errors.New("failed to reseek execute stderr")
		}
		executeRes.Stderr = f
	}

	return executeRes, nil
}

func (m *Manager) Judge(ctx context.Context, language string, copyInFileIDs map[string]string, testcase *model.TestCase, CPULimit time.Duration, memoryLimit runner.Size) (*JudgeRes, error) {
	conf, ok := m.langConf[language]
	if !ok {
		return nil, fmt.Errorf("config for %s not found", language)
	}

	executeRes, err := m.Execute(ctx, conf.ExecuteCmd, &worker.CachedFile{FileID: testcase.InputKey}, copyInFileIDs, true, CPULimit, memoryLimit)
	judgeRes := &JudgeRes{
		ExitStatus: executeRes.ExitStatus,
		Error:      executeRes.Error,
		TotalTime:  executeRes.TotalTime,
		TotalSpace: executeRes.TotalSpace,
	}
	if err != nil {
		return judgeRes, err
	}

	judgeRes.OutputId = executeRes.CachedStdoutID
	_, f := m.fs.Get(judgeRes.OutputId)
	judgeRes.Output, err = envexec.FileToReader(f)
	if err != nil {
		judgeRes.Conclusion = model.Conclusion_JudgementFailed
		return judgeRes, err
	}
	executeOutputReader, err := envexec.FileToReader(f) // can not share with judgeRes.Output
	if err != nil {
		judgeRes.Conclusion = model.Conclusion_JudgementFailed
		return judgeRes, err
	}

	_, f = m.fs.Get(testcase.OutputKey)
	testcaseOutputReader, err := envexec.FileToReader(f)
	if err != nil {
		judgeRes.Conclusion = model.Conclusion_JudgementFailed
		return judgeRes, err
	}

	ok, err = compare(testcaseOutputReader, executeOutputReader, 1024)
	if err != nil {
		judgeRes.Conclusion = model.Conclusion_JudgementFailed
		return judgeRes, err
	}

	if ok {
		judgeRes.Conclusion = model.Conclusion_Accepted
	} else {
		judgeRes.Conclusion = model.Conclusion_WrongAnswer
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

// func (m *Manager) ExecuteCommand(ctx context.Context, cmd string) string {
// 	res := <-m.worker.Execute(ctx, &worker.Request{
// 		Cmd: []worker.Cmd{{
// 			Env:         []string{"PATH=/usr/bin:/bin"},
// 			Args:        strings.Split(cmd, " "),
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
// 		}},
// 	})

// 	files := res.Results[0].Files

// 	fmt.Printf(
// 		"stdout: %s\nstderr: %s",
// 		lo.Must(io.ReadAll(files["stdout"])),
// 		lo.Must(io.ReadAll(files["stderr"])),
// 	)

// 	return fmt.Sprintf("%+v", res)
// }
