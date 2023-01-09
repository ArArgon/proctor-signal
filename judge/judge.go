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

// Compile compiles given program with specified parameters, fetches the artifact from container and stores
// it into local resource manager.
func (m *Manager) Compile(ctx context.Context, sub *model.Submission) error {
	var res worker.Response
	switch sub.Language {
	case "c":
		res = <-m.worker.Execute(ctx, &worker.Request{
			Cmd: []worker.Cmd{{
				Env:         []string{"PATH=/usr/bin:/bin"},
				Args:        []string{"gcc", "main.c", "-o", "main"},
				CPULimit:    time.Second,
				MemoryLimit: 104857600,
				ProcLimit:   50,
				Files: []worker.CmdFile{
					&worker.MemoryFile{Content: []byte("")},
					&worker.Collector{Name: "stdout", Max: 10240},
					&worker.Collector{Name: "stderr", Max: 10240},
				},
				CopyIn: map[string]worker.CmdFile{
					"main.c": &worker.MemoryFile{Content: sub.SourceCode},
				},
				CopyOut: []worker.CmdCopyOutFile{
					{"stdout", true}, {"stderr", true}, {"main.c", true}, {"main", true},
				},
			}},
		})
	}

	if stderr, err := io.ReadAll(res.Results[0].Files["stderr"]); err != io.EOF {
		return errors.New(string(stderr))
	}

	//TODO: store the artifact(res.Results[0].Files["main.c"] and res.Results[0].Files["main"]) into local resource manager

	return nil
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
				{"stdout", true}, {"stderr", true},
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
