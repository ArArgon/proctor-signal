package resource

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"proctor-signal/external/backend"
	mock_backend "proctor-signal/external/backend/mock"
	"proctor-signal/model"
	"proctor-signal/utils"
)

func genProblem(isSPJ bool) (*model.Problem, []string) {
	const (
		subtaskCount = 5
		testcaseEach = 5
	)

	var ids []string

	id := utils.GenerateID()
	ver := utils.GenerateID()
	res := &model.Problem{
		Id:         "prob-" + id,
		Ver:        "ver-" + ver,
		Kind:       model.Problem_CLASSIC,
		InputFile:  fmt.Sprintf("input-%s-%s", id, ver),
		OutputFile: fmt.Sprintf("output-%s-%s", id, ver),
	}

	if isSPJ {
		res.Kind = model.Problem_SPECIAL
		res.SpjBinaryKey = lo.ToPtr(utils.GenerateID())
		ids = append(ids, *res.SpjBinaryKey)
	}

	res.Subtasks = lo.RepeatBy(subtaskCount, func(i int) *model.Subtask {
		return &model.Subtask{
			Id:           uint32(i + 1),
			Dependencies: lo.If[[]uint32](i == 0, nil).Else([]uint32{uint32(i)}),
			TestCases: lo.RepeatBy(testcaseEach, func(j int) *model.TestCase {
				in := utils.GenerateID()
				ids = append(ids, in)
				testcase := &model.TestCase{
					Id:        uint32(j + 1),
					InputKey:  in,
					InputSize: tmpSize,
				}
				if !isSPJ {
					testcase.OutputKey = utils.GenerateID()
					testcase.OutputSize = tmpSize
					ids = append(ids, testcase.OutputKey)
				}
				return testcase
			}),
		}
	})

	return res, ids
}

func TestManager(t *testing.T) {
	cli := mock_backend.NewMockClient(gomock.NewController(t))
	logger := lo.Must(zap.NewDevelopment())
	sugar := logger.Sugar()
	fs := lo.Must(NewFileStore(logger, lo.Must(os.MkdirTemp(os.TempDir(), "signal"))))
	m := NewResourceManager(logger, cli, fs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ids := make(map[string][]string)
	idMap := make(map[string]bool)
	problemMap := make(map[string]*model.Problem)
	problems := lo.Shuffle(append(
		lo.RepeatBy(5, func(_ int) *model.Problem {
			p, i := genProblem(false)
			ids[p.Id+p.Ver] = i
			idMap = lo.Reduce(i, func(m map[string]bool, item string, _ int) map[string]bool {
				m[item] = true
				return m
			}, idMap)
			problemMap[p.Id] = p
			return p
		}),
		lo.RepeatBy(5, func(_ int) *model.Problem {
			p, i := genProblem(true)
			ids[p.Id+p.Ver] = i
			idMap = lo.Reduce(i, func(m map[string]bool, item string, _ int) map[string]bool {
				m[item] = true
				return m
			}, idMap)
			problemMap[p.Id] = p
			return p
		})...),
	)

	// Setting mock backend-client.
	cli.EXPECT().GetProblem(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context,
			in *backend.GetProblemRequest,
			_ ...grpc.CallOption,
		) (*backend.GetProblemResponse, error) {
			key := in.GetId()
			if p, ok := problemMap[key]; ok {
				return &backend.GetProblemResponse{
					StatusCode: 200,
					Data:       p,
				}, nil
			}
			sugar.Error("problem not found!: req: %+v", in)
			return &backend.GetProblemResponse{
				StatusCode: 404,
				Reason:     lo.ToPtr("problem not found"),
			}, nil
		},
	).AnyTimes()

	cli.EXPECT().GetResourceStream(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, resourceType backend.ResourceType, key string) (int64, io.ReadCloser, error) {
			if resourceType != backend.ResourceType_PROBLEM_DATA {
				return 0, nil, errors.New("must be problem data")
			}
			if _, ok := idMap[key]; !ok {
				sugar.Error("resource not found!: key: %s", key)
				return 0, nil, errors.New("404 resource not found")
			}
			return tmpSize, io.NopCloser(bytes.NewReader(randomData(t))), nil
		},
	).AnyTimes()

	// Tests.
	for _, p := range problems {
		t.Run("case-"+p.Id, func(t *testing.T) {
			rp, unlock, err := m.PrepareThenLock(ctx, p.Id, p.Ver)
			assert.NoError(t, err)
			assert.EqualValues(t, p, rp)
			org := m.problems[fromProblem(p).Key()].locks
			assert.True(t, org > 0)
			pKey := fromProblem(p).Key()

			// Check fs.
			for _, id := range ids[p.Id+p.Ver] {
				name, exeF := fs.Get(fmt.Sprintf("problem/%s/%s", pKey, id))
				assert.Equal(t, name, id)
				assert.NotNil(t, exeF)
			}

			unlock()
			assert.Equal(t, org-1, m.problems[pKey].locks)
		})
	}

	// Test problem 2
	for _, p := range problems {
		t.Run("case2-"+p.Id, func(t *testing.T) {
			rp, unlock, err := m.PrepareThenLock(ctx, p.Id, p.Ver)
			assert.NoError(t, err)
			assert.EqualValues(t, p, rp)
			org := m.problems[fromProblem(p).Key()].locks
			assert.True(t, org > 0)
			pKey := fromProblem(p).Key()

			// Check fs.
			for _, id := range ids[p.Id+p.Ver] {
				name, exeF := fs.Get(fmt.Sprintf("problem/%s/%s", pKey, id))
				assert.Equal(t, name, id)
				assert.NotNil(t, exeF)
			}

			unlock()
			assert.Equal(t, org-1, m.problems[pKey].locks)
		})
	}

	assert.NoError(t, m.evictAll())
	assert.True(t, len(m.problems) == 0)
}
