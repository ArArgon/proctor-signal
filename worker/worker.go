package worker

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/criyle/go-judge/envexec"
	"google.golang.org/protobuf/types/known/timestamppb"

	"proctor-signal/config"
	"proctor-signal/external/backend"
	"proctor-signal/judge"
	"proctor-signal/model"
	"proctor-signal/resource"

	"github.com/cenkalti/backoff/v4"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	"go.uber.org/zap"
)

type Worker struct {
	wg         *sync.WaitGroup
	judge      *judge.Manager
	resManager *resource.Manager
	backend    backend.Client
	conf       *config.Config
}

const maxTimeout = time.Minute * 10
const backoffInterval = time.Millisecond * 500

func NewWorker(
	judgeManager *judge.Manager,
	resManager *resource.Manager,
	backendCli backend.Client,
	conf *config.Config,
) *Worker {
	return &Worker{
		wg:         new(sync.WaitGroup),
		judge:      judgeManager,
		resManager: resManager,
		backend:    backendCli,
		conf:       conf,
	}
}

func (w *Worker) Start(ctx context.Context, logger *zap.Logger, concurrency int) {
	for i := 1; i <= concurrency; i++ {
		w.wg.Add(1)
		go w.spin(ctx, logger.Named("worker_spin"), i)
	}
}

func (w *Worker) spin(ctx context.Context, logger *zap.Logger, id int) {
	sugar := logger.Sugar().With("worker_id", id)
	tick := time.NewTicker(time.Millisecond * 1000)
	defer w.wg.Done()
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			sugar.Debug("context cancelled, exiting")
			return
		case <-tick.C:
			ctx, cancel := context.WithDeadline(ctx, time.Now().Add(maxTimeout))
			result, err := w.work(ctx, sugar.Named("sub_work"))
			if result != nil {
				err = multierr.Append(err, backoff.Retry(
					func() error {
						return lo.T2(w.backend.CompleteJudgeTask(ctx, result)).B
					}, backoff.WithContext(
						backoff.WithMaxRetries(backoff.NewConstantBackOff(backoffInterval), 5), ctx,
					),
				))
			}

			if err != nil {
				sugar.Errorf("an internal error occurred: %+v", err)
			}
			cancel()
		}
	}
}

func (w *Worker) work(ctx context.Context, sugar *zap.SugaredLogger) (*backend.CompleteJudgeTaskRequest, error) {
	// Fetch judge task.
	sub, abort, err := w.fetch(ctx, sugar)
	if abort {
		if err != nil && sub != nil {
			return &backend.CompleteJudgeTaskRequest{Result: &model.JudgeResult{
				SubmissionId: sub.Id,
				ProblemId:    sub.ProblemId,
				Conclusion:   model.Conclusion_InternalError,
				ErrMessage:   lo.ToPtr("failed to fetch: " + err.Error()),
			}}, err
		}
		return nil, err
	}

	result := &model.JudgeResult{
		SubmissionId: sub.Id,
		ProblemId:    sub.ProblemId,
		ReceiveTime:  timestamppb.Now(),
	}
	sugar = sugar.With(
		"submission_id", sub.Id,
		"problem_id", sub.ProblemId,
		"problem_version", sub.ProblemVer,
		"language", sub.Language,
	)
	sugar.Info("submission received!")
	defer func() {
		sugar.Infof("judgement completed, conclusion: %s", result.Conclusion.String())
	}()

	// Lock the problem (and defer unlock).
	p, unlock, err := w.resManager.PrepareThenLock(ctx, sub.ProblemId, sub.ProblemVer)
	if err != nil {
		internErr(result, "failed to prepare the problem:", err.Error())
		return &backend.CompleteJudgeTaskRequest{Result: result},
			errors.WithMessagef(err, "failed to prepare and lock problem")
	}
	defer unlock()

	outputFileCaches := make([]*os.File, 0)
	defer w.removeOutputFiles(sugar, outputFileCaches)

	// Compile source code.
	artifactIDs, abort, err := w.compile(ctx, sugar, sub, result, outputFileCaches)
	if abort {
		return &backend.CompleteJudgeTaskRequest{Result: result}, err
	}
	defer w.judge.RemoveFiles(artifactIDs)

	// Judge on the DAG.
	subtasks, err := w.judgeOnDAG(ctx, sugar, sub, p, artifactIDs, outputFileCaches)
	if err != nil {
		// internal err.
		internErr(result, "an internal error occurred during judgement:", err.Error())
		return &backend.CompleteJudgeTaskRequest{Result: result}, err
	}

	// Fill conclusion, totalSpace, totalTime.
	result.SubtaskResults = subtasks
	result.CompleteTime = timestamppb.Now()
	result.TotalTime = lo.Sum(lo.Map(subtasks, func(s *model.SubtaskResult, _ int) uint32 { return s.TotalTime }))
	result.TotalSpace = lo.Max(lo.Map(subtasks, func(s *model.SubtaskResult, _ int) float32 { return s.TotalSpace }))
	result.Conclusion = model.Conclusion_Accepted
	for _, s := range subtasks {
		result.Score += s.Score
		if s.IsRun && s.Conclusion != model.Conclusion_Accepted {
			result.Conclusion = s.Conclusion
		}
	}
	return &backend.CompleteJudgeTaskRequest{Result: result}, nil
}

func internErr(result *model.JudgeResult, messages ...string) {
	result.Conclusion = model.Conclusion_InternalError
	result.ErrMessage = lo.ToPtr(strings.Join(messages, " "))
	result.CompleteTime = timestamppb.Now()
}

func (w *Worker) fetch(ctx context.Context, sugar *zap.SugaredLogger,
) (sub *model.Submission, abort bool, err error) {
	task, err := w.backend.FetchJudgeTask(ctx, &backend.FetchJudgeTaskRequest{})
	if err != nil {
		sugar.Errorf("failed to fetch task from remote, %+v", err)
		return nil, true, errors.Wrapf(err, "failed to fetch task from remote")
	}

	// No Content.
	if task.StatusCode == 204 {
		sugar.With("reason", task.GetReason()).Debug("no content received")
		return nil, true, nil
	}

	if task.StatusCode < 200 || task.StatusCode >= 300 {
		err = errors.Errorf("backend error: %s", task.GetReason())
		sugar.Errorf("failed to fetch task from remote, server-side err: %+v", err)
		return nil, true, err
	}

	if sub = task.Task; sub == nil {
		err = errors.New("empty task")
		sugar.Errorf("invalid task: %+v", err)
		return nil, true, err
	}
	return
}

func (w *Worker) compile(
	ctx context.Context, sugar *zap.SugaredLogger,
	sub *model.Submission, result *model.JudgeResult,
	outputFileCaches []*os.File,
) (artifactIDs map[string]string, abort bool, err error) {
	compileRes, err := w.judge.Compile(ctx, sub)
	if compileRes != nil {
		outputFileCaches = append(outputFileCaches, compileRes.Stdout, compileRes.Stderr)
		_ = outputFileCaches // for lint
	}

	if err != nil {
		// Internal error.
		sugar.Errorf("an internal error occurred during compilation: %+v", err)
		if compileRes != nil {
			w.judge.RemoveFiles(compileRes.ArtifactFileIDs)
		}
		internErr(result, "failed to compile,", err.Error())
		return nil, true, err
	}

	result.ErrMessage = lo.ToPtr(compileRes.Error)

	if compileRes.StdoutSize != 0 && compileRes.StderrSize != 0 {
		result.CompilerOutput = truncateOutput(compileRes.ExecuteRes, int64(w.conf.JudgeOptions.MaxTruncatedOutput))
	} else if compileRes.StdoutSize != 0 {
		result.CompilerOutput = lo.ToPtr(truncate(compileRes.Stdout, "",
			lo.Clamp(compileRes.StdoutSize, 0, int64(w.conf.JudgeOptions.MaxTruncatedOutput)),
		))
	} else if compileRes.StderrSize != 0 {
		result.CompilerOutput = lo.ToPtr(truncate(compileRes.Stderr, "",
			lo.Clamp(compileRes.StderrSize, 0, int64(w.conf.JudgeOptions.MaxTruncatedOutput)),
		))
	} else if compileRes.Status != envexec.StatusAccepted {
		result.CompilerOutput = lo.ToPtr(compileRes.Status.String())
	}

	if compileRes.Status != envexec.StatusAccepted {
		// Compile error (not an internal error).
		sugar.With("exit_status", compileRes.Status.String()).
			Debug("failed to compile, stderr: %s", result.CompilerOutput)
		result.Conclusion = model.Conclusion_CompilationError
		return nil, true, nil
	}
	return compileRes.ArtifactFileIDs, false, nil
}

type caseOutput struct {
	caseResult *model.CaseResult
	outputFile *os.File
}

func (w *Worker) judgeOnDAG(
	ctx context.Context, sugar *zap.SugaredLogger,
	sub *model.Submission, p *model.Problem,
	artifactIDs map[string]string, outputFileCaches []*os.File,
) (subResults []*model.SubtaskResult, err error) {
	// Compose the DAG.
	dag := model.NewSubtaskGraph(p)

	// Judge on the DAG.
	score := make(map[uint32]*model.SubtaskResult, len(dag.IDs))
	for _, s := range p.Subtasks {
		score[s.Id] = &model.SubtaskResult{
			Id:          s.Id,
			IsRun:       false,
			ScorePolicy: s.ScorePolicy,
			Conclusion:  model.Conclusion_Invalid,
		}
	}

	// prepare for upload
	caseOutputCh := make(chan *caseOutput, 32)
	uploadFinishCh := make(chan struct{})

	go uploader(ctx, sugar, w.backend, uploadFinishCh, caseOutputCh)

	dag.Traverse(func(subtask *model.Subtask) bool {
		subResult := score[subtask.Id]
		subResult.IsRun = true
		subResult.Conclusion = model.Conclusion_Accepted
		subResult.CaseResults = make([]*model.CaseResult, 0, len(subtask.TestCases))

		for i, testcase := range subtask.TestCases {
			caseResult := &model.CaseResult{Id: uint32(i)}
			subResult.CaseResults = append(subResult.CaseResults, caseResult)

			var judgeRes *judge.JudgeRes
			judgeRes, err = w.judge.Judge(ctx, p, sub.Language, artifactIDs, testcase)
			if judgeRes != nil {
				outputFileCaches = append(outputFileCaches, judgeRes.Stdout, judgeRes.Stderr)
			}

			if err != nil {
				sugar.Errorf("failed to judge due to an internal error: %+v", err)
				caseResult.Conclusion = model.Conclusion_InternalError
				return false
			}

			caseResult.Conclusion = judgeRes.Conclusion
			caseResult.DiffPolicy = p.DiffPolicy
			caseResult.FinishedAt = timestamppb.Now()
			caseResult.TotalTime = uint32(judgeRes.TotalTime.Milliseconds())
			caseResult.TotalSpace = float32(judgeRes.TotalSpace.KiB()) / 1024
			caseResult.ReturnValue = int32(judgeRes.ExitStatus)
			caseResult.OutputSize = uint64(judgeRes.StdoutSize)

			// TODO: read from judge file
			if judgeRes.StdoutSize != 0 {
				buffLen := lo.Clamp(judgeRes.StdoutSize, 0, int64(w.conf.JudgeOptions.MaxTruncatedOutput))
				caseResult.TruncatedOutput = lo.ToPtr(truncate(judgeRes.Stdout, "", buffLen))
				// Upload the output data when exceeding the record's limit.
				if judgeRes.StdoutSize > buffLen {
					caseOutputCh <- &caseOutput{caseResult: caseResult, outputFile: judgeRes.Stdout}
				}
			}

			subResult.TotalTime += caseResult.TotalTime
			if subResult.TotalSpace < caseResult.TotalSpace {
				subResult.TotalSpace = caseResult.TotalSpace
			}

			// Score
			if judgeRes.Conclusion == model.Conclusion_Accepted {
				// TODO: add Testcase.Score
				caseResult.Score = subtask.Score / int32(len(subtask.TestCases))
				switch subtask.ScorePolicy {
				case model.ScorePolicy_SUM:
					subResult.Score += caseResult.Score
				case model.ScorePolicy_PCT:
					subResult.Score += caseResult.Score / int32(len(subtask.TestCases))
				case model.ScorePolicy_MIN:
					if subResult.Score > caseResult.Score {
						subResult.Score = caseResult.Score
					}
				}
				continue
			}
			subResult.Conclusion = judgeRes.Conclusion
		}
		return true
	})

	close(caseOutputCh)
	<-uploadFinishCh
	close(uploadFinishCh)
	subResults = lo.Values(score)
	return
}

func uploader(ctx context.Context, sugar *zap.SugaredLogger, backendCli backend.Client,
	uploadFinishCh chan<- struct{}, caseOutputCh <-chan *caseOutput) {
	defer func() { uploadFinishCh <- struct{}{} }()
	for {
		select {
		case <-ctx.Done():
			return
		case co, ok := <-caseOutputCh:
			if !ok {
				// upload finished
				return
			}
			err := backoff.Retry(func() error {
				if _, err := co.outputFile.Seek(0, 0); err != nil {
					sugar.Debugf("failed to re-seek judge output to the beginning: %v, retrying", err)
					return err
				}
				key, err := backendCli.PutResourceStream(ctx, backend.ResourceType_OUTPUT_DATA,
					int64(co.caseResult.OutputSize), io.NopCloser(co.outputFile))
				if err != nil {
					sugar.Debugf("failed to upload judge output: %v, retrying", err)
					return err
				}
				co.caseResult.OutputKey = key
				return nil
			}, backoff.WithContext(backoff.WithMaxRetries(backoff.NewConstantBackOff(time.Millisecond*200), 5), ctx))
			if err != nil {
				sugar.Errorf("failed to upload judge output to backend, err: %+v", err)
			}
			_ = co.outputFile.Close()
		}
	}
}

func truncateOutput(executeRes judge.ExecuteRes, outputLimit int64) *string {
	const (
		stdoutPrefix = "===== stdout ====="
		stderrPrefix = "===== stderr ====="
	)
	var (
		stdoutLimit = int64(-1)
		stderrLimit = int64(-1)
	)

	if executeRes.StdoutSize+executeRes.StderrSize > outputLimit {
		stdoutLimit = lo.Clamp(executeRes.StdoutSize, 0, outputLimit/2)
		stderrLimit = lo.Clamp(executeRes.StderrSize, 0, outputLimit/2)
	}

	return lo.ToPtr(truncate(executeRes.Stdout, stdoutPrefix, stdoutLimit) +
		truncate(executeRes.Stderr, stderrPrefix, stderrLimit))
}

func truncate(output io.Reader, prefix string, buffLen int64) string {
	var (
		err  error
		buff []byte
	)

	if buffLen != -1 {
		buff = make([]byte, buffLen)
		_, err = io.ReadFull(output, buff)
	} else {
		buff, err = io.ReadAll(output)
	}

	content := lo.Ternary(err == nil, string(buff),
		fmt.Sprintf("[internal error] failed to read content: %+v", err))
	res := lo.Ternary(prefix != "", prefix+"\n"+content+"\n", content)

	if buffLen != -1 && int64(len(res)) > buffLen {
		res = res[:buffLen-1] + "\n"
	}
	return res
}

func (w *Worker) removeOutputFiles(sugar *zap.SugaredLogger, outputFileCaches []*os.File) {
	for _, f := range outputFileCaches {
		if err := os.Remove(f.Name()); err != nil {
			sugar.With("err", err).Errorf("failed to remove file: %s", f.Name())
		}
		_ = f.Close()
	}
}

func (w *Worker) Wait() {
	w.wg.Wait()
}
