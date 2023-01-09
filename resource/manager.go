package resource

import (
	"bytes"
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"sync"

	"proctor-signal/external/backend"
	"proctor-signal/model"

	"github.com/pkg/errors"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	"go.uber.org/zap"
)

type entry struct {
	locks   int
	id      string
	version string
	problem *model.Problem
}

func fromIDVer(id, ver string) *entry {
	return &entry{id: id, version: ver}
}

func fromProblem(p *model.Problem) *entry {
	return &entry{
		id:      p.Id,
		version: p.Ver,
		problem: p,
	}
}

type problemReader struct {
	key    string
	reader io.Reader
}

func (p *entry) Key() string {
	return fmt.Sprintf("%x", sha1.Sum([]byte(p.id+"/"+p.version)))
}

func NewResourceManager(logger *zap.Logger, backend backend.BackendServiceClient, fs *FileStore) *Manager {
	return &Manager{
		fs:         fs,
		backendCli: backend,
		problems:   make(map[string]*entry),
		mut:        sync.RWMutex{},
		logger:     logger,
	}
}

type Manager struct {
	fs         *FileStore
	backendCli backend.BackendServiceClient

	problems map[string]*entry
	mut      sync.RWMutex
	logger   *zap.Logger
}

func sha(str string) string {
	if str == "" {
		return ""
	}
	return fmt.Sprintf("%x", sha1.Sum([]byte(str)))
}

func (m *Manager) fetchRes(ctx context.Context, p *model.Problem) error {
	var (
		sugar   = m.logger.Sugar().With("problem_id", p.Id, "problem_ver", p.Ver)
		fileMap map[string]string
		isSPJ   = p.GetKind() == model.Problem_SPECIAL
	)
	for _, sub := range p.Subtasks {
		for _, testcase := range sub.TestCases {
			fileMap[sha(testcase.InputKey)] = p.InputFile
			if !isSPJ {
				fileMap[sha(testcase.OutputKey)] = p.OutputFile
			}
		}
	}

	if isSPJ {
		fileMap[sha(p.GetSpjBinaryKey())] = "judge"
	}

	keys := lo.Filter(lo.Keys(fileMap), func(key string, _ int) bool { return key != "" })
	resp, err := m.backendCli.GetResourceBatch(ctx, &backend.GetResourceBatchRequest{
		Type: backend.ResourceType_PROBLEM_DATA,
		Keys: keys,
	})
	if err != nil {
		sugar.With("err", err).Errorf("failed to fetch resource from the backend")
		return errors.WithMessagef(err, "failed to fetch resource from the backend, id: %s, ver: %s", p.Id, p.Ver)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = errors.Errorf("remote err: %s", resp.GetReason())
		sugar.With("err", err).Error("the backend returns an error when fetching res")
		return errors.WithMessagef(err, "the backend returns an error, id: %s, ver: %s", p.Id, p.Ver)
	}

	// Check if all have fetched.
	if notFound, lost := lo.Find(keys, func(key string) bool { _, ok := resp.Data[key]; return ok }); lost {
		err = errors.Errorf("resource (key: %s) not found in response", notFound)
		sugar.With("err", err).Error("resource not found")
		return errors.WithMessagef(err, "problem id: %s, ver: %s", p.Id, p.Ver)
	}

	err = m.fs.saveProblem(fromProblem(p), lo.Map(keys, func(key string, _ int) *problemReader {
		return &problemReader{
			key:    key,
			reader: bytes.NewReader(resp.Data[key].Data),
		}
	}))

	if err != nil {
		sugar.With("err", err).Errorf("failed to save problem")
		return err
	}

	return nil
}

func (m *Manager) fetchProblem(ctx context.Context, id, version string) (*model.Problem, error) {
	ver := &version
	// `version` is optional.
	if version == "" {
		ver = nil
	}

	resp, err := m.backendCli.GetProblem(ctx, &backend.GetProblemRequest{
		Id:  id,
		Ver: ver,
	})

	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errors.Errorf("received a remote error when fetching problem, code: %d, reson: %s",
			resp.StatusCode, resp.GetReason(),
		)
	}

	if resp.Data == nil {
		return nil, errors.New("received a remote error when fetching problem: empty problem")
	}

	return resp.Data, nil
}

func (m *Manager) HoldAndLock(ctx context.Context, id, version string) (*model.Problem, func(), error) {
	m.mut.Lock()
	defer m.mut.Unlock()

	var (
		sugar = m.logger.Sugar().With("problem_id", id, "problem_ver", version)
		key   = fromIDVer(id, version).Key()
		err   error
		p     *model.Problem
	)

	// Fetch problem & update `version` to the latest if given empty `version`.
	if _, ok := m.problems[key]; !ok {
		p, err = m.fetchProblem(ctx, id, version)
		if err != nil {
			sugar.With("err", err).Errorf("failed to fetch problem from remote")
			return nil, nil, errors.WithMessagef(err, "failed to fetch problem from remote")
		}

		// Override default version & key.
		version = p.Ver
		key = fromProblem(p).Key()
	}

	// Entry does not exist. Verify problem & fetch resources.
	if _, ok := m.problems[key]; !ok {
		// Verify problem.
		// TODO: add cache to deter invalid problem in order to prevent flooding.
		if err = verifyProblem(p); err != nil {
			sugar.With("err", err).Errorf("problem failed to pass verification")
			return nil, nil, errors.WithMessagef(err, "problem failed to pass verification")
		}

		if err = m.fetchRes(ctx, p); err != nil {
			sugar.With("err", err).Errorf("failed to prepare source")
			return nil, nil, errors.WithMessagef(err, "failed to prepare resource for task %s @ %s", id, version)
		}
		m.problems[key] = fromProblem(p)
	}

	m.problems[key].locks++

	sugar.Info("successfully prepared and locked this problem")
	return nil, func() { m.unlock(m.problems[key].problem) }, nil
}

func (m *Manager) unlock(p *model.Problem) {
	m.mut.Lock()
	key := fromProblem(p).Key()
	if _, contains := m.problems[key]; contains {
		m.problems[key].locks--
	}
	m.mut.Unlock()
}

func (m *Manager) evictAll() error {
	sugar := m.logger.Sugar()
	m.mut.Lock()
	defer m.mut.Unlock()
	var multiErr error

	sugar.Infof("problem data eviction begins")
	for prob, ent := range m.problems {
		if ent.locks > 0 {
			continue
		}

		sugar.Infof("evicting problem %s, version %s", ent.id, ent.version)

		// Evict the problem.
		if err := m.fs.evictProblem(ent); err != nil {
			sugar.With("problem", ent).Errorln("failed to evict problem: ", err)
			multiErr = multierr.Append(multiErr, err)
			continue
		}

		// Remove entries.
		delete(m.problems, prob)
	}
	return multiErr
}

// problems/:sha(problem_id, version)/:input,:output,:spj
