package worker

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/criyle/go-judge/envexec"
	"google.golang.org/protobuf/types/known/timestamppb"

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
}

const maxTimeout = time.Minute * 10
const backoffInterval = time.Millisecond * 500

func NewWorker(
	judgeManager *judge.Manager, resManager *resource.Manager, backendCli backend.BackendServiceClient,
) *Worker {
	return &Worker{
		wg:         new(sync.WaitGroup),
		judge:      judgeManager,
		resManager: resManager,
		backend:    backendCli,
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
	task, err := w.backend.FetchJudgeTask(ctx, &backend.FetchJudgeTaskRequest{})
	if err != nil {
		sugar.With("err", err).Errorf("failed to fetch task from remote")
		return nil, err
	}

	// No Content.
	if task.StatusCode == 204 {
		sugar.With("reason", task.GetReason()).Debug("no content received")
		return nil, nil
	}

	if task.StatusCode < 200 || task.StatusCode >= 300 {
		err = errors.Errorf("backend error: %s", task.GetReason())
		sugar.With("err", err).Error("failed to fetch task from remote, server-side err")
		return nil, err
	}

	if task.Task == nil {
		err = errors.New("empty task")
		sugar.With("err", err).Error("invalid task")
		return nil, err
	}

	sub := task.Task
	receiveTime := time.Now()
	internErr := func(messages ...string) *backend.CompleteJudgeTaskRequest {
		return &backend.CompleteJudgeTaskRequest{
			Result: &model.JudgeResult{
				Conclusion:  model.Conclusion_InternalError,
				ReceiveTime: timestamppb.New(receiveTime),
				ErrMessage:  lo.ToPtr(strings.Join(messages, " ")),
			},
		}
	}

	sugar = sugar.With(
		"submission_id", sub.Id,
		"problem_id", sub.ProblemId,
		"problem_version", sub.ProblemVer,
		"language", sub.Language,
	)
	sugar.Info("submission received!")

	// Lock the problem (and defer unlock).
	p, unlock, err := w.resManager.HoldAndLock(ctx, sub.ProblemId, sub.ProblemVer)
	if err != nil {
		sugar.With("err", err).Error("failed to hold and lock problem")
		return internErr("failed to hold and lock problem,", err.Error()),
			errors.WithMessagef(err, "failed to hold and lock problem")
	}
	defer unlock()

	// Compile source code.
	compileRes, err := w.judge.Compile(ctx, sub)
	if err != nil {
		// Internal error.
		sugar.With("err", err).Error("an internal error occurred during compilation")

		if compileRes != nil {
			w.judge.RemoveFiles(compileRes.ArtifactFileIDs)
			_ = compileRes.Stdout.Close()
			_ = compileRes.Stderr.Close()
		}

		return internErr("failed to compile,", err.Error()), err
	}

	defer w.judge.RemoveFiles(compileRes.ArtifactFileIDs)
	defer func() {
		_ = compileRes.Stdout.Close()
		_ = compileRes.Stderr.Close()
	}()

	if compileRes.Status != envexec.StatusAccepted {
		// Compile error (not an internal error).
		compileErr := &backend.CompleteJudgeTaskRequest{
			Result: &model.JudgeResult{
				SubmissionId: sub.Id,
				ProblemId:    p.Id,
				ReceiveTime:  timestamppb.New(receiveTime),
			},
		}
		sugar.With("err", compileRes.Status.String()).Info("failed to compile")
		compileErr.Result.ErrMessage = lo.ToPtr(compileRes.Error)
		compileErr.Result.Conclusion = model.ConvertStatusToConclusion(compileRes.Status)
		return compileErr, nil
	}

	// Compose the DAG.
	dag := model.NewSubtaskGraph(p)

	// Judge on the DAG.
	score := make(map[uint32]*model.SubtaskResult, len(dag.IDs))
	dag.Traverse(func(subtask *model.Subtask) bool {
		subtaskResult := &model.SubtaskResult{
			Id:          subtask.Id,
			IsRun:       true,
			ScorePolicy: subtask.ScorePolicy,
			Conclusion:  model.Conclusion_Accepted,
			CaseResults: make([]*model.CaseResult, 0, len(subtask.TestCases)),
		}
		score[subtask.Id] = subtaskResult

		for _, testcase := range subtask.TestCases {
			caseResult := &model.CaseResult{Id: testcase.Id}
			subtaskResult.CaseResults = append(subtaskResult.CaseResults, caseResult)

			timeLimit := time.Duration(p.DefaultTimeLimit) * time.Millisecond
			spaceLimit := runner.Size(p.DefaultSpaceLimit * 1024 * 1024) // Megabytes.

			judgeRes, err := w.judge.Judge(
				ctx, p, sub.Language, compileRes.ArtifactFileIDs, testcase, timeLimit, spaceLimit,
			)
			if err != nil {
				sugar.With("err", err).Error("failed to judge")
				caseResult.Conclusion = model.Conclusion_InternalError
				continue
			}

			caseResult.Conclusion = judgeRes.Conclusion
			caseResult.DiffPolicy = p.DiffPolicy
			caseResult.TotalTime = uint32(judgeRes.TotalTime.Milliseconds())
			caseResult.TotalSpace = float32(judgeRes.TotalSpace.KiB())
			caseResult.ReturnValue = int32(judgeRes.ExitStatus)

			if judgeRes.OutputID != "" {
				caseResult.OutputKey = judgeRes.OutputID
				caseResult.OutputSize = uint64(judgeRes.OutputSize)
				caseResult.TruncatedOutput = &judgeRes.TruncatedOutput
			} else {
				caseResult.TruncatedOutput = lo.ToPtr("<failed to read output>")
			}

			subtaskResult.TotalTime += caseResult.TotalTime
			if subtaskResult.TotalSpace < caseResult.TotalSpace {
				subtaskResult.TotalSpace = caseResult.TotalSpace
			}

			// Score
			if judgeRes.Conclusion == model.Conclusion_Accepted {
				// TODO: add Testcase.Score
				caseResult.Score = subtask.Score / int32(len(subtask.TestCases))
				switch subtask.ScorePolicy {
				case model.ScorePolicy_SUM:
					subtaskResult.Score += caseResult.Score
				case model.ScorePolicy_PCT:
					subtaskResult.Score += caseResult.Score / int32(len(subtask.TestCases))
				case model.ScorePolicy_MIN:
					if subtaskResult.Score > caseResult.Score {
						subtaskResult.Score = caseResult.Score
					}
				}
				continue
			}
			subtaskResult.Conclusion = judgeRes.Conclusion
		}

		return true
	})

	// Final score.
	result := &model.JudgeResult{
		SubmissionId: sub.Id,
		ProblemId:    sub.ProblemId,
		ReceiveTime:  timestamppb.New(receiveTime),
		CompleteTime: timestamppb.Now(),
		Conclusion:   model.Conclusion_Accepted,
	}
	for _, id := range dag.IDs {
		if s, ok := score[id]; ok {
			if s.Conclusion != model.Conclusion_Accepted {
				result.Conclusion = s.Conclusion
			}
			continue
		}
		// Not scored.
		score[id] = &model.SubtaskResult{
			Id:    id,
			IsRun: false,
		}
	}
	result.SubtaskResults = lo.Values(score)

	// read compile output
	bytes, err := io.ReadAll(compileRes.Stdout)
	if err != nil {
		sugar.With("err", compileRes.Status.String()).Error("failed to read compile output")
		result.CompilerOutput = lo.ToPtr("failed to read compile output")
	} else {
		result.CompilerOutput = lo.ToPtr(string(bytes))
	}

	// Calculate the result.TotalTime & result.TotalSpace
	for _, subtaskResult := range result.SubtaskResults {
		if !subtaskResult.IsRun {
			continue
		}

		result.TotalTime += subtaskResult.TotalTime
		if result.TotalSpace < subtaskResult.TotalSpace {
			result.TotalSpace = subtaskResult.TotalSpace
		}
	}

	sugar.Infof("judgement succeed, conclusion: %s", result.Conclusion.String())
	return &backend.CompleteJudgeTaskRequest{Result: result}, nil
}

func (w *Worker) Wait() {
	w.wg.Wait()
}
