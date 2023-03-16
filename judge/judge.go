package judge

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"
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
	Stdout     *os.File
	StdoutSize int64
	Stderr     *os.File
	StderrSize int64
	TotalTime  time.Duration
	TotalSpace runner.Size
}

type CompileRes struct {
	ExecuteRes      // ignore output size
	ArtifactFileIDs map[string]string
}

type JudgeRes struct {
	ExecuteRes
	Conclusion model.Conclusion
}

func (m *Manager) RemoveFiles(fileIDs map[string]string) {
	if fileIDs == nil {
		return
	}
	_, err := m.fs.BulkRemove(lo.Values(fileIDs))
	if err != nil {
		m.logger.Sugar().With("file_ids", fileIDs).Errorf("failed to remove files, %+v", err)
	}
}

func (m *Manager) Compile(ctx context.Context, sub *model.Submission) (*CompileRes, error) {
	compileConf, ok := m.lang[sub.Language]
	if !ok {
		return nil, fmt.Errorf("compile config for %s not found", sub.Language)
	}

	if !compileConf.needCompile() {
		// New file in fileStore.
		srcFile, err := m.fs.New()
		if err != nil {
			return nil, err
		}

		// Write source code into it.
		_, err = srcFile.Write(sub.SourceCode)
		if err != nil {
			return nil, err
		}

		// Add this file to fileStore with name `compileConf.getSourceName()`.
		srcId, err := m.fs.Add(compileConf.getSourceName(), srcFile.Name())
		if err != nil {
			return nil, err
		}

		// Compose result with source code as an artifact.
		return &CompileRes{
			ExecuteRes:      ExecuteRes{Status: envexec.StatusAccepted},
			ArtifactFileIDs: map[string]string{compileConf.getSourceName(): srcId},
		}, nil
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
	var (
		f    *os.File
		size int64
	)
	if f, ok = result.Files["stdout"]; ok {
		size, err = reseekAndGetSize(f)
		if err != nil {
			return compileRes, err
		}
		compileRes.Stdout = f
		compileRes.StdoutSize = size
	}
	if f, ok = result.Files["stderr"]; ok {
		size, err = reseekAndGetSize(f)
		if err != nil {
			return compileRes, err
		}
		compileRes.Stderr = f
		compileRes.StderrSize = size
	}

	// cache files
	compileRes.ArtifactFileIDs = result.FileIDs
	if _, ok = result.FileIDs[compileConf.getArtifactName()]; !ok && result.Status == envexec.StatusAccepted {
		return compileRes, errors.New("failed to cache ArtifactFile")
	}

	return compileRes, nil
}

// Execute executes cmd with stdin and copyIn files.
func (m *Manager) Execute(ctx context.Context, cmd string, stdin worker.CmdFile,
	copyInFileIDs map[string]string, CPULimit time.Duration, memoryLimit runner.Size) (*ExecuteRes, error) {
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
	if f, ok := result.Files["stdout"]; ok {
		size, err := reseekAndGetSize(f)
		if err != nil {
			return executeRes, err
		}
		executeRes.Stdout = f
		executeRes.StdoutSize = size
	}

	if f, ok := result.Files["stderr"]; ok {
		size, err := reseekAndGetSize(f)
		if err != nil {
			return executeRes, err
		}
		executeRes.Stderr = f
		executeRes.StderrSize = size
	}

	return executeRes, nil
}

func reseekAndGetSize(f *os.File) (int64, error) {
	if _, err := f.Seek(0, 0); err != nil {
		return 0, errors.Errorf("failed to re-seek file %s", f.Name())
	}
	fi, err := f.Stat()
	if err != nil {
		return 0, errors.Errorf("failed to read stat of %s", f.Name())
	}
	return fi.Size(), nil
}

func fsKey(p *model.Problem, key string) string {
	binFileKey := key
	if p != nil && p.Id != "" && p.Ver != "" {
		binFileKey = resource.ResKey(p, binFileKey)
	}

	return binFileKey
}

func (m *Manager) Judge(
	ctx context.Context, p *model.Problem, language string,
	copyInFileIDs map[string]string, testcase *model.TestCase,
) (*JudgeRes, error) {
	conf, ok := m.lang[language]
	if !ok {
		return nil, fmt.Errorf("config for %s not found", language)
	}

	executeRes, err := m.Execute(
		ctx, strings.Join(conf.getRunCmd(), " "),
		&worker.CachedFile{FileID: fsKey(p, testcase.InputKey)}, copyInFileIDs,
		time.Duration(p.DefaultTimeLimit)*time.Millisecond*time.Duration(conf.raw.ResourceFactor),
		runner.Size(p.DefaultSpaceLimit*1024*1024)*runner.Size(conf.raw.ResourceFactor),
	)
	if executeRes == nil {
		return nil, err
	}

	judgeRes := &JudgeRes{
		ExecuteRes: *executeRes,
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

	// Only judge on executeRes.Stdout, ignore executeRes.Stderr
	switch p.DiffPolicy {
	case model.DiffPolicy_FLOAT:
		ok, err = compareFloats(testcaseOutputReader, executeRes.Stdout, int(*p.FloatEps))
	case model.DiffPolicy_LINE:
		ok, err = compareLines(testcaseOutputReader, executeRes.Stdout, p.IgnoreNewline, getMd5())
	default:
		ok, err = compareAll(testcaseOutputReader, executeRes.Stdout, 1024)
	}

	if err != nil {
		judgeRes.Conclusion = model.Conclusion_InternalError
		return judgeRes, err
	}

	judgeRes.Conclusion = lo.Ternary(ok, model.Conclusion_Accepted, model.Conclusion_WrongAnswer)

	// reset executeRes.Stdout
	if _, err = executeRes.Stdout.Seek(0, 0); err != nil {
		return judgeRes, err
	}

	return judgeRes, nil
}
