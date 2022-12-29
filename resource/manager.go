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
}

func fromProblem(p *model.Problem) *entry {
	return &entry{
		id:      p.Id,
		version: p.Ver,
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

func (m *Manager) fetchRes(ctx context.Context, p *model.Problem) error {
	var (
		sugar   = m.logger.Sugar().With("problem_id", p.Id, "problem_ver", p.Ver)
		fileMap map[string]string
		isSPJ   = p.GetKind() == model.Problem_SPECIAL
	)
	for _, sub := range p.Subtasks {
		for _, testcase := range sub.TestCases {
			fileMap[testcase.InputKey] = p.InputFile
			if !isSPJ {
				fileMap[testcase.OutputKey] = p.OutputFile
			}
		}
	}

	if isSPJ {
		fileMap[p.GetSpjBinaryKey()] = "judge"
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

	err = m.fs.SaveProblem(fromProblem(p), lo.Map(keys, func(key string, _ int) *problemReader {
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

func (m *Manager) PrepareAndLock(ctx context.Context, p *model.Problem) error {
	m.mut.Lock()
	defer m.mut.Unlock()

	var (
		id, version = p.Id, p.Ver
		sugar       = m.logger.Sugar().With("problem_id", id, "problem_ver", version)
		ent         = fromProblem(p)
		key         = ent.Key()
	)

	if _, contains := m.problems[key]; contains {
		m.problems[key].locks++
		sugar.Info("problem already exists, lock count inc: ", m.problems[key].locks)
		return nil
	}

	if err := m.fetchRes(ctx, p); err != nil {
		sugar.With("err", err).Errorf("failed to prepare source")
		return errors.WithMessagef(err, "failed to prepare resource for task %s @ %s", id, version)
	}

	ent.locks = 1
	m.problems[key] = ent

	sugar.Info("successfully prepared and locked this problem")
	return nil
}

func (m *Manager) Unlock(p *model.Problem) {
	m.mut.Lock()
	e := entry{id: p.Id, version: p.Ver}
	key := e.Key()
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
		if err := m.fs.EvictProblem(ent); err != nil {
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
