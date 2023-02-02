package resource

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"

	"proctor-signal/external/backend"
	"proctor-signal/model"

	"github.com/pkg/errors"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	"go.uber.org/zap"
)

const concurrency = 10
const backoffInterval = time.Millisecond * 200

type entry struct {
	locks   int
	id      string
	version string
	problem *model.Problem
}

func resStoreKey(ent *entry, key string) string {
	return ent.Key() + "/" + key
}

func ResKey(p *model.Problem, key string) string {
	return "problem/" + resStoreKey(fromProblem(p), key)
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

type sourceReader struct {
	key    string
	size   int64
	reader io.ReadCloser
}

func (p *entry) Key() string {
	return fmt.Sprintf("%x", sha1.Sum([]byte(p.id+"/"+p.version)))
}

func NewResourceManager(logger *zap.Logger, backend backend.Client, fs *FileStore) *Manager {
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
	backendCli backend.Client

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

func fetchWorker(
	ctx context.Context, backendCli backend.Client,
	wg *sync.WaitGroup, keyChan <-chan string, resChan chan<- *sourceReader, errChan chan<- error,
	sugar *zap.SugaredLogger,
) {
	sugar.Info("fetching worker started")
	defer wg.Done()
	for {
		select {
		case <-ctx.Done():
			sugar.Info("context cancelled, exiting")
			return
		case key, ok := <-keyChan:
			if !ok {
				sugar.Info("channel closed, exiting")
				return
			}
			err := backoff.Retry(
				func() error {
					size, reader, err := backendCli.GetResourceStream(ctx, backend.ResourceType_PROBLEM_DATA, key)
					if err != nil {
						return err
					}
					resChan <- &sourceReader{
						key:    key,
						size:   size,
						reader: reader,
					}
					return nil
				}, backoff.WithContext(
					backoff.WithMaxRetries(backoff.NewConstantBackOff(backoffInterval), 5), ctx,
				),
			)
			if err != nil {
				sugar.With("err", err).Errorf("failed to get resource")
				errChan <- errors.WithMessagef(err, "failed to get resource, key %s", key)
				return
			}
		}
	}
}

func (m *Manager) fetchRes(ctx context.Context, p *model.Problem) error {
	var (
		sugar   = m.logger.Sugar().With("problem_id", p.Id, "problem_ver", p.Ver)
		resKeys = make(map[string]bool)
		isSPJ   = p.GetKind() == model.Problem_SPECIAL
	)

	// Collect resources to fetch.
	for _, sub := range p.Subtasks {
		for _, testcase := range sub.TestCases {
			resKeys[testcase.InputKey] = true
			if !isSPJ {
				resKeys[testcase.OutputKey] = true
			}
		}
	}
	if isSPJ {
		resKeys[p.GetSpjBinaryKey()] = true
	}
	delete(resKeys, "")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	keys := lo.Filter(lo.Keys(resKeys), func(key string, _ int) bool { return key != "" })
	wg := new(sync.WaitGroup)
	keyChan := make(chan string, len(keys))
	resChan := make(chan *sourceReader, concurrency)
	errChan := make(chan error)
	finRecv := make(chan struct{}, 1)
	results := make([]*sourceReader, 0, len(keys))

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go fetchWorker(ctx, m.backendCli, wg, keyChan, resChan, errChan, sugar.Named("fetch-worker"))
	}

	// Result receiver.
	go func() {
		for res := range resChan {
			results = append(results, res)
		}
		finRecv <- struct{}{}
	}()

	var err error
	// Err handler.
	go func() {
		for recvErr := range errChan {
			err = multierr.Append(err, recvErr)
			sugar.Errorf("received an error, killing all workers")
			cancel()
		}
	}()

	// Close all readers.
	defer func() {
		for _, r := range results {
			_ = r.reader.Close()
		}
	}()

	for _, key := range keys {
		keyChan <- key
	}
	close(keyChan)

	wg.Wait()
	close(errChan)
	close(resChan)

	<-finRecv

	// Failed to fetch resource.
	if err != nil {
		sugar.With("err", err).Error("failed to fetch resource")
		return errors.WithMessagef(err, "failed to fetch resource")
	}

	// Check if all have fetched.
	if len(keys) != len(results) {
		err = errors.New("some resource(s) are missing")
		sugar.Errorf("some resource(s) are missing, expecting: %d, got: %d", len(keys), len(results))
		return errors.WithMessagef(err, "problem id: %s, ver: %s", p.Id, p.Ver)
	}

	if err = m.fs.saveProblem(fromProblem(p), results); err != nil {
		sugar.With("err", err).Errorf("failed to save problem")
		return errors.WithMessagef(err, "failed to save problem")
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

// HoldAndLock prepares the given problem if it does not exist in the fileStore and locks that problem
// by increasing its lock semaphore. If HoldAndLock() returns a nil error, user should call the unlock
// function in the return values once the problem is no longer needed.
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

		// Override default version & key. This is needed when the `version` is omitted.
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
			sugar.With("err", err).Errorf("failed to prepare the resource")
			return nil, nil, errors.WithMessagef(err, "failed to prepare resource for task %s @ %s", id, version)
		}
		m.problems[key] = fromProblem(p)
	}

	m.problems[key].locks++

	sugar.Info("successfully prepared and locked this problem")
	return m.problems[key].problem, func() { m.unlock(m.problems[key].problem) }, nil
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
