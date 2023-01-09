package judge

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

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
func (m *Manager) Compile(ctx context.Context) {
	// TODO
	panic("implement me")
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
