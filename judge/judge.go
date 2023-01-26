package judge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"proctor-signal/model"
	"proctor-signal/resource"

	"github.com/criyle/go-judge/envexec"
	"github.com/criyle/go-judge/worker"
	"github.com/criyle/go-sandbox/runner"
	"github.com/samber/lo"
	"gopkg.in/yaml.v3"
)

func NewJudgeManager(worker worker.Worker) *Manager {
	return &Manager{
		worker: worker,
	}
}

type Manager struct {
	worker     worker.Worker
	resManager *resource.Manager
	fs         *resource.FileStore // share with worker
}

type CompileOption struct {
}

type CompileRes struct {
	Status         envexec.Status
	ExitStatus     int
	Error          string
	Output         []byte
	TotalTime      time.Duration
	TotalSpace     runner.Size
	ArtifactFileId string
}

type ExecuteRes struct {
	Status     envexec.Status
	ExitStatus int
	Error      string
	Output     []byte
	TotalTime  time.Duration
	TotalSpace runner.Size
}

type JudgeRes struct {
	Status     envexec.Status
	ExitStatus int
	Error      string
	Output     []byte
	TotalTime  time.Duration
	TotalSpace runner.Size
}

var languageConfig map[string]struct {
	SourceName   string            `yaml:"SourceName"`
	ArtifactName string            `yaml:"ArtifactName"`
	CompileCmd   string            `yaml:"CompileCmd"`
	ExecuteCmd   string            `yaml:"ExecuteCmd"`
	Options      map[string]string `yaml:"Options"`
}

func LoadLanguageConfig(configPath string) {
	f, err := os.Open(configPath)
	defer func() { _ = f.Close() }()

	if err != nil {
		fmt.Printf("err: %v\n", err)
		panic("fail to open language config")
	}
	err = yaml.NewDecoder(f).Decode(&languageConfig)
	if err != nil {
		fmt.Printf("err: %v\n", err)
		panic("fail to decode language config")
	}
}

func (m *Manager) RemoveFiles(fileIDs []string) {
	for _, v := range fileIDs {
		m.fs.Remove(v)
	}
}

func (m *Manager) Compile(ctx context.Context, p *model.Problem, sub *model.Submission) (*CompileRes, error) {
	// TODO: compile options
	compileConf, ok := languageConfig[sub.Language]
	if !ok {
		return nil, fmt.Errorf("compile config for %s not found", sub.Language)
	}

	res := <-m.worker.Execute(ctx, &worker.Request{
		Cmd: []worker.Cmd{{
			Env:         []string{"PATH=/usr/bin:/bin", "SourceName=" + compileConf.SourceName, "ArtifactName=" + compileConf.ArtifactName},
			Args:        strings.Split(compileConf.CompileCmd, " "),
			CPULimit:    time.Duration(p.DefaultTimeLimit),
			MemoryLimit: runner.Size(p.DefaultSpaceLimit),
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

	compileRes.ArtifactFileId, ok = res.Results[0].FileIDs[compileConf.ArtifactName]
	if !ok {
		return compileRes, errors.New("failed to cache ArtifactFileName")
	}

	if res.Error != nil {
		return compileRes, res.Error
	}

	// read compile output
	var err error
	if res.Results[0].ExitStatus == 0 {
		_, err = res.Results[0].Files["stdout"].Seek(0, 0)
		if err != nil {
			return compileRes, errors.New("failed to reseek compile stdout")
		}
		compileRes.Output, err = io.ReadAll(res.Results[0].Files["stdout"])
		if err != nil && err != io.EOF {
			return compileRes, errors.New("failed to read compile stdout")
		}
	} else {
		_, err = res.Results[0].Files["stderr"].Seek(0, 0)
		if err != nil {
			return compileRes, errors.New("failed to reseek compile stderr")
		}
		compileRes.Output, err = io.ReadAll(res.Results[0].Files["stderr"])
		if err != nil && err != io.EOF {
			return compileRes, errors.New("failed to read compile stderr")
		}
	}

	return compileRes, nil
}

// ExecuteFile execute a runnable file with stdin.
func (m *Manager) ExecuteFile(ctx context.Context, fileID string, stdin worker.CmdFile, CPULimit time.Duration, memoryLimit runner.Size) (*ExecuteRes, error) {
	name, _ := m.fs.Get(fileID)
	if name == "" {
		return nil, errors.New("failed to get runnable file with id: " + fileID)
	}

	res := <-m.worker.Execute(ctx, &worker.Request{
		Cmd: []worker.Cmd{{
			Env:         []string{"PATH=/usr/bin:/bin"},
			Args:        []string{name},
			CPULimit:    CPULimit,
			MemoryLimit: memoryLimit,
			ProcLimit:   50,
			Files: []worker.CmdFile{
				stdin,
				&worker.Collector{Name: "stdout", Max: 10240},
				&worker.Collector{Name: "stderr", Max: 10240},
			},
			CopyIn: map[string]worker.CmdFile{
				name: &worker.CachedFile{FileID: fileID},
			},
			CopyOut: []worker.CmdCopyOutFile{
				{Name: "stdout", Optional: true},
				{Name: "stderr", Optional: true},
			},
		}},
	})

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
	if res.Results[0].ExitStatus == 0 {
		_, err = res.Results[0].Files["stdout"].Seek(0, 0)
		if err != nil {
			return executeRes, errors.New("failed to reseek execute stdout: " + err.Error())
		}
		executeRes.Output, err = io.ReadAll(res.Results[0].Files["stdout"])
		if err != nil && err != io.EOF {
			return executeRes, errors.New("failed to read execute stdout: " + err.Error())
		}
	} else {
		_, err = res.Results[0].Files["stderr"].Seek(0, 0)
		if err != nil {
			return executeRes, errors.New("failed to reseek execute stderr")
		}
		executeRes.Output, err = io.ReadAll(res.Results[0].Files["stderr"])
		if err != nil && err != io.EOF {
			return executeRes, errors.New("failed to read execute stderr")
		}
	}

	return executeRes, nil
}

// func (m *Manager) Judge(ctx context.Context, fileID string, testcase *model.TestCase, CPULimit time.Duration, memoryLimit runner.Size) {
// 	executeRes, err := m.ExecuteFile(ctx, fileID, &worker.CachedFile{FileID: testcase.InputKey}, CPULimit, memoryLimit)
// 	if err != nil {
// 		return
// 	}

// 	_, f := m.fs.Get(testcase.OutputKey)
// 	r, err := envexec.FileToReader(f)
// 	if err != nil {
// 		return
// 	}

// 	expectedOuput, err := io.ReadAll(r)
// 	if err != nil {
// 		return
// 	}

// }

func (m *Manager) ExecuteCommand(ctx context.Context, cmd string) string {
	res := <-m.worker.Execute(ctx, &worker.Request{
		Cmd: []worker.Cmd{{
			Env:         []string{"PATH=/usr/bin:/bin"},
			Args:        strings.Split(cmd, " "),
			CPULimit:    time.Second,
			MemoryLimit: 104857600,
			ProcLimit:   50,
			Files: []worker.CmdFile{
				&worker.MemoryFile{Content: []byte("")},
				&worker.Collector{Name: "stdout", Max: 10240},
				&worker.Collector{Name: "stderr", Max: 10240},
			},
			CopyOut: []worker.CmdCopyOutFile{
				{Name: "stdout", Optional: true},
				{Name: "stderr", Optional: true},
			},
		}},
	})

	files := res.Results[0].Files

	fmt.Printf(
		"stdout: %s\nstderr: %s",
		lo.Must(io.ReadAll(files["stdout"])),
		lo.Must(io.ReadAll(files["stderr"])),
	)

	return fmt.Sprintf("%+v", res)
}
