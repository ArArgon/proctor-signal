package worker

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

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
	judge      *judge.Manager
	resManager *resource.Manager
	backend    backend.BackendServiceClient
}

const maxTimeout = time.Minute * 10
const backoffInterval = time.Millisecond * 200

func (w *Worker) Start(ctx context.Context, logger *zap.Logger, concurrency int) {
	for i := 1; i <= concurrency; i++ {
		go w.spin(ctx, logger, i)
	}
}

func (w *Worker) spin(ctx context.Context, logger *zap.Logger, id int) {
	sugar := logger.Sugar().With("worker_id", id).Named("worker_spin")
	tick := time.NewTicker(time.Millisecond * 500)
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

	if task.StatusCode < 200 || task.StatusCode >= 200 {
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
	internErr := &backend.CompleteJudgeTaskRequest{
		Result: &model.JudgeResult{
			Conclusion:  model.Conclusion_InternalError,
			ReceiveTime: timestamppb.New(receiveTime),
			Remark:      lo.ToPtr(err.Error()),
		},
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
		return internErr, errors.WithMessagef(err, "failed to hold and lock problem")
	}
	defer unlock()

	// TODO: Compile source code.
	fileIDs, err := w.judge.Compile(ctx, p, sub)
	defer w.judge.RemoveFiles(fileIDs)
	if err != nil {
		return internErr, err
	}

	// Compose the DAG.
	dag := model.NewSubtaskGraph(p)

	// Judge on the DAG.
	score := make(map[uint32]*model.SubtaskResult, len(dag.IDs))
	dag.Traverse(func(subtask *model.Subtask) bool {
		// TODO: Compose testing CMD.

		// TODO: Execute CMD.

		// TODO: Score this subtask.

		// TODO: Return opinion.

		return true
	})

	// Final score.
	result := &model.JudgeResult{
		SubmissionId:   sub.Id,
		ProblemId:      sub.ProblemId,
		ReceiveTime:    timestamppb.New(receiveTime),
		CompleteTime:   timestamppb.Now(),
		CompilerOutput: nil,
	}
	for _, id := range dag.IDs {
		if _, ok := score[id]; ok {
			continue
		}
		// Not scored.
		score[id] = &model.SubtaskResult{
			Id:    id,
			IsRun: false,
		}
	}

	result.SubtaskResults = lo.Values(score)
	// TODO: embed compiler's output and other info.

	// TODO: Report.

	return nil, nil
}
