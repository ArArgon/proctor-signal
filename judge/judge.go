package judge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"proctor-signal/model"
	"proctor-signal/resource"

	"github.com/criyle/go-judge/worker"
	"github.com/criyle/go-sandbox/runner"
	"github.com/samber/lo"
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

func (m *Manager) RemoveFiles(fileIDs map[string]string) {
	for _, v := range fileIDs {
		m.fs.Remove(v)
	}
}

// Compile compiles given program with specified parameters, fetches the artifact from container and stores
// it into local resource manager.
func (m *Manager) Compile(ctx context.Context, p *model.Problem, sub *model.Submission) (map[string]string, error) {
	var res worker.Response
	switch sub.Language {
	case "c":
		res = <-m.worker.Execute(ctx, &worker.Request{
			Cmd: []worker.Cmd{{
				Env:         []string{"PATH=/usr/bin:/bin"},
				Args:        append([]string{"gcc", "main.c", "-o", "main"}, strings.Split(sub.CompilerOption, " ")...),
				CPULimit:    time.Duration(p.DefaultTimeLimit),
				MemoryLimit: runner.Size(p.DefaultSpaceLimit),
				ProcLimit:   50,
				Files: []worker.CmdFile{
					&worker.MemoryFile{Content: []byte("")},
					&worker.Collector{Name: "stdout", Max: 10240},
					&worker.Collector{Name: "stderr", Max: 10240},
				},
				CopyIn: map[string]worker.CmdFile{
					"main.c": &worker.MemoryFile{Content: sub.SourceCode},
				},
				CopyOutCached: []worker.CmdCopyOutFile{
					{"main", true},
				},
				CopyOut: []worker.CmdCopyOutFile{
					{"stderr", true},
				},
			}},
		})
		// case "cpp":
		// 	res = <-m.worker.Execute(ctx, &worker.Request{
		// 		Cmd: []worker.Cmd{{
		// 			Env:         []string{"PATH=/usr/bin:/bin"},
		// 			Args:        []string{"g++", "main.cpp", "-o", "main"},
		// 			CPULimit:    time.Duration(p.DefaultTimeLimit),
		// 			MemoryLimit: runner.Size(p.DefaultSpaceLimit),
		// 			ProcLimit:   50,
		// 			Files: []worker.CmdFile{
		// 				&worker.MemoryFile{Content: []byte("")},
		// 				&worker.Collector{Name: "stdout", Max: 10240},
		// 				&worker.Collector{Name: "stderr", Max: 10240},
		// 			},
		// 			CopyIn: map[string]worker.CmdFile{
		// 				"main.cpp": &worker.MemoryFile{Content: sub.SourceCode},
		// 			},
		// 			CopyOut: []worker.CmdCopyOutFile{
		// 				{"stderr", true},
		// 			},
		// 		}},
		// 	})
		// 	var id string
		// 	id, err := m.fs.Add("main.cpp", "")
		// 	if err != nil {
		// 		return err
		// 	}
		// 	cleanList = append(cleanList, id)

		// 	id, err = m.fs.Add("main", "")
		// 	if err != nil {
		// 		return err
		// 	}
		// 	cleanList = append(cleanList, id)
		// case "java":
		// 	res = <-m.worker.Execute(ctx, &worker.Request{
		// 		Cmd: []worker.Cmd{{
		// 			Env:         []string{"PATH=/usr/bin:/bin"},
		// 			Args:        []string{"javac", "main.java"},
		// 			CPULimit:    time.Duration(p.DefaultTimeLimit),
		// 			MemoryLimit: runner.Size(p.DefaultSpaceLimit),
		// 			ProcLimit:   50,
		// 			Files: []worker.CmdFile{
		// 				&worker.MemoryFile{Content: []byte("")},
		// 				&worker.Collector{Name: "stdout", Max: 10240},
		// 				&worker.Collector{Name: "stderr", Max: 10240},
		// 			},
		// 			CopyIn: map[string]worker.CmdFile{
		// 				"main.java": &worker.MemoryFile{Content: sub.SourceCode},
		// 			},
		// 			CopyOut: []worker.CmdCopyOutFile{
		// 				{"stderr", true},
		// 			},
		// 		}},
		// 	})
		// 	var id string
		// 	id, err := m.fs.Add("main.java", "")
		// 	if err != nil {
		// 		return err
		// 	}
		// 	cleanList = append(cleanList, id)

		// 	id, err = m.fs.Add("main.class", "")
		// 	if err != nil {
		// 		return err
		// 	}
		// 	cleanList = append(cleanList, id)
		// case "golang":
		// 	res = <-m.worker.Execute(ctx, &worker.Request{
		// 		Cmd: []worker.Cmd{{
		// 			Env:         []string{"PATH=/usr/bin:/bin"},
		// 			Args:        []string{"go", "build", "main.go"},
		// 			CPULimit:    time.Duration(p.DefaultTimeLimit),
		// 			MemoryLimit: runner.Size(p.DefaultSpaceLimit),
		// 			ProcLimit:   50,
		// 			Files: []worker.CmdFile{
		// 				&worker.MemoryFile{Content: []byte("")},
		// 				&worker.Collector{Name: "stdout", Max: 10240},
		// 				&worker.Collector{Name: "stderr", Max: 10240},
		// 			},
		// 			CopyIn: map[string]worker.CmdFile{
		// 				"main.go": &worker.MemoryFile{Content: sub.SourceCode},
		// 			},
		// 			CopyOut: []worker.CmdCopyOutFile{
		// 				{"stderr", true},
		// 			},
		// 		}},
		// 	})
		// 	var id string
		// 	id, err := m.fs.Add("main.go", "")
		// 	if err != nil {
		// 		return err
		// 	}
		// 	cleanList = append(cleanList, id)

		// 	id, err = m.fs.Add("main", "")
		// 	if err != nil {
		// 		return err
		// 	}
		// 	cleanList = append(cleanList, id)
	}

	if stderr, err := io.ReadAll(res.Results[0].Files["stderr"]); err != io.EOF {
		return res.Results[0].FileIDs, errors.New(string(stderr))
	}
	return res.Results[0].FileIDs, nil
}

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
