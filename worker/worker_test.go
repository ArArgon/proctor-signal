package worker

import (
	"bufio"
	"bytes"
	"context"
	rand2 "crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"os"
	"regexp"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	"proctor-signal/external/backend"
	mock_backend "proctor-signal/external/backend/mock"
	"proctor-signal/model"
	"proctor-signal/utils"
)

func Test_truncate(t *testing.T) {
	data := []byte(lo.RandomString(20480, []rune(utils.AlphaNumericTable)))
	prefix := utils.GenerateID()

	// No prefix.
	assert.EqualValues(t, data, truncate(bytes.NewReader(data), "", -1))
	assert.EqualValues(t, data[:1024], truncate(bytes.NewReader(data), "", 1024))

	// With prefix.
	assert.Equal(t,
		prefix+"\n"+string(data)+"\n",
		truncate(bytes.NewReader(data), prefix, -1),
	)
	tmp := truncate(bytes.NewReader(data), prefix, 128)
	assert.Equal(t, len(tmp), 128)
	assert.Regexp(t, regexp.MustCompile(`^`+prefix+"\n.*\n$"), tmp)
}

func Test_uploader(t *testing.T) {
	const testcaseSize = 30
	const maximumFileSize = 1 * 1024 * 1024 // 1 MiB

	type testOutput struct {
		*caseOutput
		expectSha string
	}

	backendCli := mock_backend.NewMockClient(gomock.NewController(t))
	ctx, cancel := context.WithCancel(context.Background())
	zapConfig := zap.NewDevelopmentConfig()
	zapConfig.DisableStacktrace = true
	sugar := lo.Must(zapConfig.Build()).Sugar()
	defer cancel()

	keyHash := make(map[string]string, testcaseSize)
	backendCli.
		EXPECT().
		PutResourceStream(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(
			func(ctx context.Context, resourceType backend.ResourceType,
				size int64, body io.ReadCloser) (string, error) {
				defer func() { _ = body.Close() }()
				// 10% chance of failing.
				if rand.Int()%10 == 1 {
					return "", errors.New("just failed.")
				}

				key := utils.GenerateID()
				if resourceType != backend.ResourceType_OUTPUT_DATA {
					return "", errors.Errorf("illegal resource type: %s", resourceType.String())
				}

				hash := sha256.New()
				reader := bufio.NewReader(body)
				assert.EqualValues(t, size, lo.Must(reader.WriteTo(hash)))

				keyHash[key] = fmt.Sprintf("%x", hash.Sum(nil))
				return key, nil
			},
		).AnyTimes()

	// prepare for upload
	caseOutputCh := make(chan *caseOutput, 32)
	uploadFinishCh := make(chan struct{})
	go uploader(ctx, sugar, backendCli, uploadFinishCh, caseOutputCh)

	// Compose payload.
	payloads := make([]testOutput, 0, testcaseSize)
	temps := make([]*os.File, 0, testcaseSize)
	defer func() {
		for _, tmp := range temps {
			_ = os.Remove(tmp.Name())
		}
	}()

	for i := 0; i < testcaseSize; i++ {
		hash := sha256.New()
		size := lo.Clamp(rand.Int63(), 10240, maximumFileSize)
		file := lo.Must(os.CreateTemp("", fmt.Sprintf("tmp-file-output-%d-", i)))
		lo.Must(io.CopyN(io.MultiWriter(file, hash), rand2.Reader, size))
		temps = append(temps, file)

		payload := testOutput{
			caseOutput: &caseOutput{
				caseResult: &model.CaseResult{
					OutputSize: uint64(size),
				},
				outputFile: file,
			},
			expectSha: fmt.Sprintf("%x", hash.Sum(nil)),
		}

		payloads = append(payloads, payload)
		caseOutputCh <- payload.caseOutput
	}

	close(caseOutputCh)
	<-uploadFinishCh
	close(uploadFinishCh)

	// Verification.
	assert.Equal(t, len(keyHash), len(payloads))
	for _, payload := range payloads {
		assert.Equal(t, payload.expectSha, keyHash[payload.caseResult.OutputKey])
	}
}
