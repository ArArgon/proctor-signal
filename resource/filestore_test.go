package resource

import (
	"bytes"
	"crypto/sha1"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/criyle/go-judge/envexec"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"proctor-signal/utils"
)

const tmpSize = 5 * 1048576

func randomData(t *testing.T) []byte {
	res := make([]byte, tmpSize)
	sz, err := rand.Read(res)
	assert.NoError(t, err)
	assert.Equal(t, sz, tmpSize)
	return res
}

func TestFileStore(t *testing.T) {
	// Init logger.
	config := zap.NewDevelopmentConfig()
	config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	config.Level.SetLevel(zap.InfoLevel)
	logger := lo.Must(config.Build())
	//sugar := logger.Sugar()

	// Prepare tmp dir.
	loc, err := os.MkdirTemp(os.TempDir(), "signal")
	assert.NoError(t, err)
	defer func() {
		assert.NoError(t, os.RemoveAll(loc))
	}()

	// Init fs.
	fs := lo.Must(NewFileStore(logger, loc))

	// Test temporary data.
	t.Run("temporary-data", func(t *testing.T) {
		size := 25
		ids := make([]string, 0, size)
		idToName := make(map[string]string, size)
		idToSha := make(map[string]string, size)

		for i := 0; i < size; i++ {
			name := utils.GenerateID()
			file := lo.Must(fs.New())
			dat := randomData(t)

			assert.Equal(t, lo.T2(tmpSize, error(nil)), lo.T2(file.Write(dat)))
			id := lo.Must(fs.Add(name, file.Name()))
			idToName[id] = name
			ids = append(ids, id)
			idToSha[id] = fmt.Sprintf("%x", sha1.Sum(dat))
		}

		// List (name -> id).
		assert.Equal(t, idToName, fs.List())

		for id, name := range idToName {
			n, f := fs.Get(id)
			assert.Equal(t, name, n)
			assert.IsType(t, &envexec.FileInput{}, f)
			file := lo.Must(os.Open(f.(*envexec.FileInput).Path))
			assert.NotNil(t, file)
			assert.Equal(t, idToSha[id], fmt.Sprintf("%x", sha1.Sum(lo.Must(io.ReadAll(file)))))
		}

		// Remove.
		lo.RepeatBy(5, func(i int) string {
			id := ids[i*5]
			assert.True(t, fs.Remove(id))
			delete(idToName, id)
			delete(idToSha, id)
			return id
		})
		assert.Equal(t, idToName, fs.List())
		// Cannot remove a file twice.
		assert.False(t, fs.Remove(ids[0]))
	})

	// Test problem.
	t.Run("problem", func(t *testing.T) {
		testSize := 5
		entries := make([]*entry, 0, testSize)
		files := make(map[string][]*problemReader)
		hash := make(map[string]string)

		// Prepare problems and data.
		for i := 0; i < testSize; i++ {
			ent := &entry{
				id:      utils.GenerateID(),
				version: utils.GenerateID(),
			}
			entries = append(entries, ent)
			filesCnt := lo.Clamp(rand.Intn(20), 1, 20)
			readers := lo.RepeatBy(filesCnt, func(index int) *problemReader {
				dat := randomData(t)
				key := utils.GenerateID()
				hash[ent.Key()+"/"+key] = fmt.Sprintf("%x", sha1.Sum(dat))
				return &problemReader{
					key:    key,
					reader: bytes.NewReader(dat),
				}
			})
			files[ent.Key()] = readers
			assert.NoError(t, fs.saveProblem(ent, readers))
		}

		// Ignore duplicated data.
		assert.ErrorContains(t, fs.saveProblem(entries[0], files[entries[0].Key()]), "problem already exists")

		// Read data.
		for _, ent := range entries {
			res := files[ent.Key()]
			assert.DirExists(t, filepath.Join(loc, "persistent", ent.Key()))
			for _, f := range res {
				// ID: problem/:problem_key/:test_file_key
				p := fmt.Sprintf("problem/%s/%s", ent.Key(), f.key)
				fileKey, f2 := fs.Get(p)
				assert.Equal(t, fileKey, f.key)
				assert.True(t, f2 != nil)
				file := lo.Must(os.Open(f2.(*envexec.FileInput).Path))
				assert.Equal(t,
					hash[ent.Key()+"/"+f.key],
					fmt.Sprintf("%x", sha1.Sum(lo.Must(io.ReadAll(file)))),
				)
				assert.NoError(t, file.Close())
			}
		}

		// Eviction.
		for _, ent := range entries {
			assert.NoError(t, fs.evictProblem(ent))
			assert.NoDirExists(t, filepath.Join(loc, "persistent", ent.Key()))
			res := files[ent.Key()]
			for _, f := range res {
				// ID: problem/:problem_key/:test_file_key
				p := fmt.Sprintf("problem/%s/%s", ent.Key(), f.key)
				f1, f2 := fs.Get(p)
				assert.Equal(t, f1, "")
				assert.True(t, f2 == nil)
			}
		}
	})
}