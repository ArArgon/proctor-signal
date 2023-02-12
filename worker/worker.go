package worker

import (
	"context"
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
	"github.com/criyle/go-sandbox/runner"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	"go.uber.org/zap"
)

type Worker struct {
	wg         *sync.WaitGroup
	judge      *judge.Manager
	resManager *resource.Manager
	backend    backend.BackendServiceClient
	conf       *config.Config
}

const maxTimeout = time.Minute * 10
const backoffInterval = time.Millisecond * 500

func NewWorker(
	judgeManager *judge.Manager, resManager *resource.Manager, backendCli backend.BackendServiceClient, conf *config.Config,
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
			sugar.Info("context cancelled, exiting")
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
				sugar.With("err", err).Error("an internal error occurred")
			}
			cancel()
		}
	}
}

func (w *Worker) work(ctx context.Context, sugar *zap.SugaredLogger) (*backend.CompleteJudgeTaskRequest, error) {
	// Fetch judge task.
	sub, abort, err := w.fetch(ctx, sugar)
	if abort {
		if err != nil {
			return &backend.CompleteJudgeTaskRequest{Result: &model.JudgeResult{
				Conclusion: model.Conclusion_InternalError,
				ErrMessage: lo.ToPtr("failed to fetch: " + err.Error()),
			}}, err
		}
		return nil, err
	}

	result := &model.JudgeResult{
		SubmissionId: sub.Id,
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
	p, unlock, err := w.resManager.HoldAndLock(ctx, sub.ProblemId, sub.ProblemVer)
	if err != nil {
		sugar.With("err", err).Error("failed to hold and lock problem")
		internErr(result, "failed to hold and lock problem,", err.Error())
		return &backend.CompleteJudgeTaskRequest{Result: result},
			errors.WithMessagef(err, "failed to hold and lock problem")
	}
	defer unlock()

	outputFileCaches := make([]*os.File, 0)
	defer w.removeOutputFiles(sugar, outputFileCaches)
	// TODO: upload output files to OSS before it was deleted

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
		internErr(result, "an internal error occured during judgement: ", err.Error())
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
	result.ErrMessage = lo.ToPtr(strings.Join(messages, ""))
	result.CompleteTime = timestamppb.Now()
}

func (w *Worker) fetch(ctx context.Context, sugar *zap.SugaredLogger,
) (sub *model.Submission, abort bool, err error) {
	task, err := w.backend.FetchJudgeTask(ctx, &backend.FetchJudgeTaskRequest{})
	if err != nil {
		sugar.With("err", err).Errorf("failed to fetch task from remote")
		return nil, true, err
	}

	// No Content.
	if task.StatusCode == 204 {
		sugar.With("reason", task.GetReason()).Debug("no content received")
		return nil, true, nil
	}

	if task.StatusCode < 200 || task.StatusCode >= 300 {
		err = errors.Errorf("backend error: %s", task.GetReason())
		sugar.With("err", err).Error("failed to fetch task from remote, server-side err")
		return nil, true, err
	}

	if sub = task.Task; sub == nil {
		err = errors.New("empty task")
		sugar.With("err", err).Error("invalid task")
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
	}

	if err != nil {
		// Internal error.
		sugar.With("err", err).Error("an internal error occurred during compilation")
		w.judge.RemoveFiles(compileRes.ArtifactFileIDs)

		internErr(result, "failed to compile,", err.Error())
		return nil, true, err
	}

	result.ErrMessage = lo.ToPtr(compileRes.Error)
	result.CompilerOutput = w.truncatedOutput(compileRes.ExecuteRes)

	if compileRes.Status != envexec.StatusAccepted {
		// Compile error (not an internal error).
		sugar.With("err", compileRes.Status.String()).Debug("failed to compile")
		result.Conclusion = model.Conclusion_CompilationError
		return nil, true, nil
	}
	return compileRes.ArtifactFileIDs, false, nil
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

	dag.Traverse(func(subtask *model.Subtask) bool {
		subResult := score[subtask.Id]
		subResult.IsRun = true
		subResult.Conclusion = model.Conclusion_Accepted
		subResult.CaseResults = make([]*model.CaseResult, 0, len(subtask.TestCases))

		for i, testcase := range subtask.TestCases {
			caseResult := &model.CaseResult{Id: uint32(i)}
			subResult.CaseResults = append(subResult.CaseResults, caseResult)

			timeLimit := time.Duration(p.DefaultTimeLimit) * time.Millisecond
			spaceLimit := runner.Size(p.DefaultSpaceLimit * 1024 * 1024) // Megabytes.

			var judgeRes *judge.JudgeRes
			judgeRes, err = w.judge.Judge(
				ctx, p, sub.Language, artifactIDs, testcase, timeLimit, spaceLimit,
			)
			if judgeRes != nil {
				outputFileCaches = append(outputFileCaches, judgeRes.Stdout, judgeRes.Stderr)
			}

			if err != nil {
				sugar.With("err", err).Error("failed to judge")
				caseResult.Conclusion = model.Conclusion_InternalError
				return false
			}

			caseResult.Conclusion = judgeRes.Conclusion
			caseResult.DiffPolicy = p.DiffPolicy
			caseResult.TotalTime = uint32(judgeRes.TotalTime.Milliseconds())
			caseResult.TotalSpace = float32(judgeRes.TotalSpace.KiB()) / 1024
			caseResult.ReturnValue = int32(judgeRes.ExitStatus)

			judgeRes.StderrSize = 0 // ignore stderr
			caseResult.TruncatedOutput = w.truncatedOutput(judgeRes.ExecuteRes)

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

	subResults = lo.Values(score)
	return
}

func (w *Worker) truncatedOutput(executeRes judge.ExecuteRes) *string {
	var output string
	if executeRes.StdoutSize+executeRes.StderrSize > int64(w.conf.JudgeOptions.MaxTruncatedOutput) {
		if executeRes.StdoutSize > int64(w.conf.JudgeOptions.MaxTruncatedOutput)/2 &&
			executeRes.StderrSize < int64(w.conf.JudgeOptions.MaxTruncatedOutput)/2 {
			// read all executeRes.Stderr
			buff := make([]byte, int64(w.conf.JudgeOptions.MaxTruncatedOutput)-executeRes.StderrSize)
			if _, err := io.ReadFull(executeRes.Stdout, buff); err != nil {
				output = "===stdout:\nfailed to read stdout\n"
			} else {
				output = "===stdout:\n" + string(buff) + "\n"
			}

			if executeRes.StderrSize != 0 {
				if buff, err := io.ReadAll(executeRes.Stderr); err != nil {
					output += "===stderr:\nfailed to read stderr\n"
				} else {
					output += "===stderr:\n" + string(buff) + "\n"
				}
			}
		} else if executeRes.StdoutSize < int64(w.conf.JudgeOptions.MaxTruncatedOutput)/2 &&
			executeRes.StderrSize > int64(w.conf.JudgeOptions.MaxTruncatedOutput)/2 {
			// read all executeRes.Stdout
			if executeRes.StdoutSize != 0 {
				if buff, err := io.ReadAll(executeRes.Stdout); err != nil {
					output = "===stdout:\nfailed to read stdout\n"
				} else {
					output = "===stdout:\n" + string(buff) + "\n"
				}
			}

			buff := make([]byte, int64(w.conf.JudgeOptions.MaxTruncatedOutput)-executeRes.StdoutSize)
			if _, err := io.ReadFull(executeRes.Stdout, buff); err != nil {
				output = "===stdout:\nfailed to read stdout\n"
			} else {
				output = "===stdout:\n" + string(buff) + "\n"
			}
		}
	} else {
		// read all
		if executeRes.StdoutSize != 0 {
			if buff, err := io.ReadAll(executeRes.Stdout); err != nil {
				output = "===stdout:\nfailed to read stdout\n"
			} else {
				output = "===stdout:\n" + string(buff) + "\n"
			}
		}

		if executeRes.StderrSize != 0 {
			if buff, err := io.ReadAll(executeRes.Stderr); err != nil {
				output += "===stderr:\nfailed to read stderr\n"
			} else {
				output += "===stderr:\n" + string(buff) + "\n"
			}
		}
	}
	return &output
}

func (w *Worker) removeOutputFiles(sugar *zap.SugaredLogger, outputFileCaches []*os.File) {
	for _, f := range outputFileCaches {
		fi, err := f.Stat()
		f.Close()
		if err != nil {
			sugar.With("err", err).Errorf("failed to remove file: %+v", f)
			continue
		}
		if err := os.Remove(fi.Name()); err != nil {
			sugar.With("err", err).Errorf("failed to remove file: %s", fi.Name())
		}
	}
}

func (w *Worker) Wait() {
	w.wg.Wait()
}
