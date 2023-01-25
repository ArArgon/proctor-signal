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
	fs         *resource.FileStore
}

type CompileOption struct {
}

type CompileRes struct {
	Status          envexec.Status
	ExitStatus      int
	Error           string
	Output          string
	ArtifactFileIDs map[string]string
}

type ExecuteRes struct {
	Status     envexec.Status
	ExitStatus int
	Error      string
	Output     string
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

func (m *Manager) RemoveFiles(fileIDs map[string]string) {
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
		Status:          res.Results[0].Status,
		ExitStatus:      res.Results[0].ExitStatus,
		Error:           res.Results[0].Error,
		ArtifactFileIDs: res.Results[0].FileIDs,
	}
	if res.Error != nil {
		return compileRes, res.Error
	}

	// read compile output
	var compileOutput []byte
	var err error
	if res.Results[0].ExitStatus == 0 {
		_, err = res.Results[0].Files["stdout"].Seek(0, 0)
		if err != nil {
			return compileRes, errors.New("failed to reseek compile stdout")
		}
		compileOutput, err = io.ReadAll(res.Results[0].Files["stdout"])
		if err != nil && err != io.EOF {
			return compileRes, errors.New("failed to read compile stdout")
		}
	} else {
		_, err = res.Results[0].Files["stderr"].Seek(0, 0)
		if err != nil {
			return compileRes, errors.New("failed to reseek compile stderr")
		}
		compileOutput, err = io.ReadAll(res.Results[0].Files["stderr"])
		if err != nil && err != io.EOF {
			return compileRes, errors.New("failed to read compile stderr")
		}
	}
	compileRes.Output = string(compileOutput)

	return compileRes, nil
}

// ExecuteFile execute a runnable file with stdin.
func (m *Manager) ExecuteFile(ctx context.Context, filename, fileID string, input worker.CmdFile, p *model.Problem) (*ExecuteRes, error) {
	res := <-m.worker.Execute(ctx, &worker.Request{
		Cmd: []worker.Cmd{{
			Env:         []string{"PATH=/usr/bin:/bin"},
			Args:        []string{filename},
			CPULimit:    time.Duration(p.DefaultTimeLimit),
			MemoryLimit: runner.Size(p.DefaultSpaceLimit),
			ProcLimit:   50,
			Files: []worker.CmdFile{
				// &worker.MemoryFile{Content: []byte("")},
				input,
				&worker.Collector{Name: "stdout", Max: 10240},
				&worker.Collector{Name: "stderr", Max: 10240},
			},
			CopyIn: map[string]worker.CmdFile{
				filename: &worker.CachedFile{FileID: fileID},
				// "input":  input,
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
	}
	if res.Error != nil {
		return executeRes, res.Error
	}

	// read execute output
	var executeOutput []byte
	var err error
	if res.Results[0].ExitStatus == 0 {
		_, err = res.Results[0].Files["stdout"].Seek(0, 0)
		if err != nil {
			return executeRes, errors.New("failed to reseek execute stdout: " + err.Error())
		}
		executeOutput, err = io.ReadAll(res.Results[0].Files["stdout"])
		if err != nil && err != io.EOF {
			return executeRes, errors.New("failed to read execute stdout: " + err.Error())
		}
	} else {
		_, err = res.Results[0].Files["stderr"].Seek(0, 0)
		if err != nil {
			return executeRes, errors.New("failed to reseek execute stderr")
		}
		executeOutput, err = io.ReadAll(res.Results[0].Files["stderr"])
		if err != nil && err != io.EOF {
			return executeRes, errors.New("failed to read execute stderr")
		}
	}
	executeRes.Output = string(executeOutput)

	return executeRes, nil
}

// func (m *Manager) ExecuteCmd(ctx context.Context, cmd string) string {
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
// 	for _, f := range files {
// 		_, _ = f.Seek(0, 0)
// 	}

// 	return fmt.Sprintf(
// 		"stdout: %s\nstderr: %s",
// 		lo.Must(io.ReadAll(files["stdout"])),
// 		lo.Must(io.ReadAll(files["stderr"])),
// 	)
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
