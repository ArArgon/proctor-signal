package backend

import (
	"context"
	"io"
	"path/filepath"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"go.uber.org/zap"

	"proctor-signal/config"
	"proctor-signal/utils"
)

const keyRandomSize = 32
const localDateFormat = "2006-01-02"

type s3Client struct {
	cli        *minio.Client
	sugar      *zap.SugaredLogger
	bucketName string
}

func (s *s3Client) GetResourceStream(ctx context.Context,
	resourceType ResourceType, key string) (int64, io.ReadCloser, error) {
	if resourceType != ResourceType_PROBLEM_DATA {
		return 0, nil, errors.Errorf("forbidden to download %s type of resource", resourceType.String())
	}
	// Compose resource objectName.
	objectName := filepath.Join(resourceType.String(), key)

	res, err := s.cli.GetObject(ctx, s.bucketName, objectName, minio.GetObjectOptions{})
	if err != nil {
		return 0, nil, errors.Wrapf(err, "failed to get resource `%s`", key)
	}
	stat, err := res.Stat()
	if err != nil {
		return 0, nil, errors.Wrapf(err, "failed to get stat of `%s`", key)
	}
	return stat.Size, res, nil
}

func (s *s3Client) PutResourceStream(
	ctx context.Context, resourceType ResourceType, size int64, body io.ReadCloser,
) (string, error) {
	if resourceType != ResourceType_OUTPUT_DATA {
		return "", errors.Errorf("forbidden to upload %s type of resource", resourceType.String())
	}
	// Compose resource key.
	key := time.Now().Format(localDateFormat) + "-" + lo.RandomString(keyRandomSize, []rune(utils.AlphaNumericTable))
	objectName := filepath.Join(resourceType.String(), key)

	_, err := s.cli.PutObject(ctx, s.bucketName, objectName, body, size, minio.PutObjectOptions{
		ContentType:     "application/text",
		ContentEncoding: "gzip",
	})
	if err != nil {
		return "", errors.Wrapf(err, "failed to upload `%s`", key)
	}

	s.sugar.Debugf("successfully uploaded resource `%s` to the tos", key)
	return key, nil
}

func newS3ResourceClient(ctx context.Context, logger *zap.Logger, conf *config.Config) (*s3Client, error) {
	cli, err := minio.New(conf.OSS.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(conf.OSS.AccessKeyID, conf.OSS.SecretAccessKey, ""),
		Secure: conf.OSS.UseTLS,
		Region: conf.OSS.Region,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to initialize minio s3 client at %s", conf.OSS.Endpoint)
	}

	bucketName := conf.OSS.BucketName
	bucketExists, err := cli.BucketExists(ctx, bucketName)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to verify the status of buckect %s", bucketName)
	}
	if !bucketExists {
		return nil, errors.Errorf("bucket %s does not exist", bucketName)
	}

	return &s3Client{
		cli:        cli,
		sugar:      logger.Sugar().Named("s3-client"),
		bucketName: bucketName,
	}, nil
}
