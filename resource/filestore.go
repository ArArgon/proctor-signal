package resource

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"proctor-signal/utils"

	"github.com/criyle/go-judge/envexec"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"github.com/shirou/gopsutil/disk"
	"go.uber.org/multierr"
	"go.uber.org/zap"
)

type FileStore struct {
	rootLoc string

	tmpLoc   string
	tmpFiles map[string]string

	probLoc   string
	probFiles map[string]string

	logger *zap.Logger
	mut    sync.RWMutex
}

func NewFileStore(logger *zap.Logger, loc string) (*FileStore, error) {
	sugar := logger.Sugar().With("location", loc)
	var err error

	sugar.Info("mkdir tmp")
	if err = os.MkdirAll(path.Join(loc, "tmp"), 0755); err != nil {
		sugar.With("err", err).Error("failed to create directory tmp")
		return nil, err
	}

	sugar.Info("mkdir persistent")
	if err = os.MkdirAll(path.Join(loc, "persistent"), 0755); err != nil {
		sugar.With("err", err).Error("failed to create directory persistent")
		return nil, err
	}

	return &FileStore{
		rootLoc:   loc,
		tmpLoc:    path.Join(loc, "tmp"),
		probLoc:   path.Join(loc, "persistent"),
		tmpFiles:  make(map[string]string),
		probFiles: make(map[string]string),
		logger:    logger,
		mut:       sync.RWMutex{},
	}, nil
}

func (m *FileStore) SaveProblem(ent *entry, readers []*problemReader) error {
	var (
		sugar = m.logger.Sugar().With("problem_id", ent.id, "problem_ver", ent.version)
		loc   = path.Join(m.probLoc, ent.Key())
		err   error
		buf   = make([]byte, 512)
	)

	sugar.Infof("dumping problem with %d files at %s", len(readers), loc)

	defer func() {
		if err != nil {
			// Rollback.
			sugar.Warnf("rolling back, removing %s", loc)
			err = multierr.Append(err, os.RemoveAll(loc))
		}
	}()

	for _, r := range readers {
		var f *os.File
		resPath := path.Join(loc, r.key)

		f, err = os.OpenFile(resPath, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0644)

		if errors.Is(err, os.ErrExist) {
			// File already exists, ignore.
			sugar.Infof("ignore file %s: already exists", resPath)
			continue
		}

		if err != nil {
			sugar.With("err", err).Errorf("failed to open a new file for %s", r.key)
			return errors.WithMessagef(err, "failed to open a new file for %s", r.key)
		}

		_, err = io.CopyBuffer(f, r.reader, buf)
		if err != nil {
			_ = f.Close()
			sugar.With("err", err).Errorf("failed to save file %s", resPath)
			return errors.WithMessagef(err, "failed to save file %s", resPath)
		}

		_ = f.Close()
		sugar.Infof("successfully saved file %s", resPath)
	}

	return nil
}

func (m *FileStore) EvictProblem(p *entry) error {
	var (
		key   = p.Key()
		sugar = m.logger.Sugar().With("problem_id", p.id, "problem_ver", p.version)
		loc   = path.Join(m.probLoc, key)
	)

	m.mut.Lock()
	defer m.mut.RLock()

	files, err := os.ReadDir(loc)
	if err != nil {
		sugar.With("err", err).Errorf("failed to evict problem")
		return errors.WithMessagef(err, "failed to evict problem %s, ver: %s", p.id, p.version)
	}

	for _, f := range files {
		// Ignore directories.
		if !f.Type().IsRegular() {
			continue
		}

		fileKey := key + "/" + f.Name()
		delete(m.probFiles, fileKey)
	}

	// Remove directory.
	if err = os.RemoveAll(loc); err != nil {
		sugar.With("err", err).Errorf("failed to remove directory %s", loc)
		return errors.WithMessagef(err, "failed to remove directory %s", loc)
	}

	return nil
}

func (m *FileStore) GetSpaceInfo() (*disk.UsageStat, error) {
	return disk.Usage(m.rootLoc)
}

// Add adds a new file into the tmp section.
func (m *FileStore) Add(name, path string) (string, error) {
	m.mut.Lock()
	defer m.mut.Unlock()

	if m.tmpLoc == filepath.Dir(path) {
		id := filepath.Base(path)
		m.tmpFiles[id] = name
		return id, nil
	}
	return "", fmt.Errorf("faild to add %s into fileStore: does not have prefix %s", path, m.tmpLoc)
}

// Remove removes one tmpFile and returns whether it is deleted. Should you want to evict staled problems,
// consider RemoveProblemFile() or EvictProblem() instead.
func (m *FileStore) Remove(id string) bool {
	m.mut.Lock()
	defer m.mut.Unlock()

	loc := path.Join(m.tmpLoc, id)
	if _, ok := m.tmpFiles[id]; !ok {
		return false
	}

	if err := os.Remove(loc); !errors.Is(err, os.ErrNotExist) {
		m.logger.Sugar().With("err", err).Errorf("failed to remove file [id: %s] [loc: %s]", id, err)
		return false
	}

	delete(m.tmpFiles, id)
	return true
}

// Get retrieve the file with given id and its name. Names of problem cases could be identical, e.g.
//
// fe4aec942dd62c5b58d94b025ca2e15c7e49b2c5/1	-> problem.in (case 1)
// d0c5e3fe827505b65dd18a3503d38d48290900c4/2	-> problem.in (case 2)
//
// However, names of tmp files are guaranteed to be unique.
func (m *FileStore) Get(id string) (string, envexec.File) {
	m.mut.RLock()
	defer m.mut.RUnlock()

	var (
		loc  string
		name string
		ok   bool
	)

	if strings.HasPrefix("problem/", id) {
		// problem/:problem_key/:data_key
		parts := strings.Split(id, "/")
		if len(parts) != 3 {
			m.logger.Sugar().Errorf("failed to get a problem file, invalid id: %s", id)
			return "", nil
		}

		key := path.Join(parts[1], parts[2])
		loc = path.Join(m.probLoc, key)

		if name, ok = m.probFiles[key]; !ok {
			m.logger.Sugar().Infof("file not found: %s", key)
			return "", nil
		}

		if _, err := os.Stat(loc); os.IsNotExist(err) {
			m.logger.Sugar().Errorf("missing file in cache: %s", key)
			return "", nil
		}
	} else {
		loc = path.Join(m.tmpLoc, id)
		if _, err := os.Stat(loc); os.IsNotExist(err) {
			return "", nil
		}
		if name, ok = m.tmpFiles[id]; !ok {
			name = id
		}
	}

	return name, envexec.NewFileInput(loc)
}

// List returns all temporary data.
func (m *FileStore) List() map[string]string {
	m.mut.RLock()
	defer m.mut.RUnlock()

	entries, err := os.ReadDir(m.tmpLoc)
	if err != nil {
		return nil
	}

	names := make(map[string]string, len(entries))
	for _, f := range entries {
		if f.IsDir() {
			continue
		}
		names[f.Name()] = m.tmpFiles[f.Name()]
	}
	return names
}

// New creates an empty file descriptor under tmpdir.
func (m *FileStore) New() (*os.File, error) {
	var (
		res   *os.File
		sugar = m.logger.Sugar()
	)
	_, err := lo.AttemptWhile(5, func(_ int) (error, bool) {
		var (
			id  = utils.GenerateID()
			err error
		)
		res, err = os.OpenFile(path.Join(m.tmpLoc, id), os.O_CREATE|os.O_RDWR|os.O_EXCL, 0644)
		if err == nil {
			return nil, false
		}

		// ID already taken, try another one.
		if !errors.Is(err, os.ErrExist) {
			return err, true
		}

		// Fatal err.
		return nil, false
	})
	if err != nil {
		sugar.Errorln("failed to new a file in fileStore: ", err)
		return nil, err
	}
	return res, nil
}
