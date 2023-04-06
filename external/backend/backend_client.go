package backend

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"proctor-signal/config"
)

const pingInterval = time.Second * 5
const pingRetryInterval = time.Millisecond * 500

type ResourceClient interface {
	// GetResourceStream fetches resource from the backend and returns the size, the reader of data.
	// User must close the body once it's no longer needed. This method utilizes HTTP, and is expected
	// to consume less memory.
	GetResourceStream(ctx context.Context, resourceType ResourceType, key string) (int64, io.ReadCloser, error)

	// PutResourceStream upload resource to the backend in stream. This method utilizes HTTP, and is
	// expected to consume less memory.
	PutResourceStream(ctx context.Context, resourceType ResourceType, size int64, body io.ReadCloser) (string, error)
}

type Client interface {
	BackendServiceClient
	ResourceClient
	// ReportExit informs the backend of the exit of this executor instance.
	ReportExit(ctx context.Context, reason string) error
}

type client struct {
	BackendServiceClient
	ResourceClient
	auth *authManager
}

// NewBackendClient builds a backend.Client with given configurations.
func NewBackendClient(
	ctx context.Context, logger *zap.Logger, conf *config.Config, cancel context.CancelFunc,
) (Client, error) {
	// gRPC connections.
	cred := lo.Ternary(conf.Backend.InsecureGrpc,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithTransportCredentials(credentials.NewTLS(new(tls.Config))),
	)
	rpcAddr := fmt.Sprintf("dns:%s:%d", conf.Backend.Addr, conf.Backend.GrpcPort)
	sugar := logger.Sugar()

	// Auth conn & client.
	authConn, err := grpc.Dial(rpcAddr, cred)
	if err != nil {
		sugar.Errorf("failed to establish auth grpc connection, %+v", err)
		return nil, errors.Wrapf(err, "failed to establish auth grpc connection")
	}

	auth, err := newAuthManager(ctx, logger, authConn, conf)
	if err != nil {
		return nil, err
	}
	sugar.Info("authentication success")

	// Backend conn & client.
	backendConn, err := grpc.Dial(rpcAddr,
		cred,
		grpc.WithUnaryInterceptor(auth.unaryInterceptor()),
		grpc.WithStreamInterceptor(auth.streamInterceptor()),
	)
	if err != nil {
		sugar.Errorf("failed to establish backend grpc connection, %+v", err)
		return nil, errors.Wrapf(err, "failed to establish backend grpc connection")
	}
	sugar.Info("successfully connect to the grpc backend")

	// Create resource client.
	resCli, err := newResourceClient(ctx, logger, conf, auth)
	if err != nil {
		sugar.Errorf("failed to create a resource client, %+v", err)
		return nil, err
	}

	cli := &client{
		ResourceClient:       resCli,
		BackendServiceClient: NewBackendServiceClient(backendConn),
		auth:                 auth,
	}
	go cli.ping(ctx, sugar, cancel)
	return cli, nil
}

func newResourceClient(
	ctx context.Context, logger *zap.Logger, conf *config.Config, auth *authManager,
) (ResourceClient, error) {
	switch conf.Storage {
	case "http":
		return newHttpResClient(auth, conf)
	case "s3":
		return newS3ResourceClient(ctx, logger, conf)
	case "grpc":
		return nil, errors.New("grpc resource client is currently unsupported")
	}
	return nil, errors.Errorf("unknown resource client type: %s", conf.Storage)
}

func (c *client) ReportExit(ctx context.Context, reason string) error {
	if err := c.auth.reportExit(ctx, reason); err != nil {
		return errors.WithMessage(err, "failed to inform the backend server")
	}
	return nil
}

func (c *client) ping(ctx context.Context, sugar *zap.SugaredLogger, cancel context.CancelFunc) {
	tick := time.NewTicker(pingInterval)
	for {
		select {
		case <-tick.C:
			err := backoff.Retry(
				func() error {
					resp, err := c.Ping(ctx, &PingRequest{})
					if err != nil {
						return err
					}
					if resp.StatusCode < 200 || resp.StatusCode >= 300 {
						return errors.Errorf("ping error, message: %s", resp.GetMessage())
					}
					return nil
				}, backoff.WithContext(
					backoff.WithMaxRetries(backoff.NewConstantBackOff(pingRetryInterval), 5), ctx,
				),
			)
			if err != nil {
				// Halt.
				sugar.Errorf("failed to ping, disconnecting from the backend, %+v", err)
				cancel()
			}
		case <-ctx.Done():
			tick.Stop()
			return
		}
	}
}
