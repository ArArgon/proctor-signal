package backend

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/pkg/errors"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"proctor-signal/config"
)

type Client interface {
	BackendServiceClient

	// GetResourceStream fetches resource from the backend and returns the size, the reader of data.
	// User must close the body once it's no longer needed. This method utilizes HTTP, and is expected
	// to consume less memory.
	GetResourceStream(ctx context.Context, resourceType ResourceType, key string) (int64, io.ReadCloser, error)

	// PutResourceStream upload resource to the backend in stream. This method utilizes HTTP, and is
	// expected to consume less memory.
	PutResourceStream(ctx context.Context, resourceType ResourceType, size int64, body io.ReadCloser) (string, error)

	// ReportExit informs the backend of the exit of this executor instance.
	ReportExit(ctx context.Context, reason string)
}

type client struct {
	BackendServiceClient
	auth    *authManager
	httpCli *http.Client
	baseURL string
}

// NewBackendClient builds a backend.Client with given configurations.
func NewBackendClient(ctx context.Context, logger *zap.Logger, conf *config.Config) (Client, error) {
	// gRPC connections.
	cred := lo.Ternary(conf.Backend.InsecureGrpc,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithTransportCredentials(credentials.NewTLS(new(tls.Config))),
	)
	rpcAddr := fmt.Sprintf("dns:%s:%d", conf.Backend.Addr, conf.Backend.GrpcPort)

	// Auth conn & client.
	authConn, err := grpc.Dial(rpcAddr, cred)
	if err != nil {
		return nil, errors.WithMessage(err, "failed to establish auth grpc connection")
	}

	auth, err := newAuthManager(ctx, logger, authConn, conf)
	if err != nil {
		return nil, errors.WithMessage(err, "failed to initialize auth client")
	}

	// Backend conn & client.
	backendConn, err := grpc.Dial(rpcAddr,
		cred,
		grpc.WithUnaryInterceptor(auth.unaryInterceptor()),
		grpc.WithStreamInterceptor(auth.streamInterceptor()),
	)
	if err != nil {
		return nil, errors.WithMessage(err, "failed to establish backend grpc connection")
	}

	baseURL, err := url.JoinPath(
		fmt.Sprintf("%s://%s:%d",
			lo.Ternary(conf.Backend.HttpTLS, "https", "http"),
			conf.Backend.Addr, conf.Backend.HttpPort,
		),
		conf.Backend.HttpPrefix,
	)
	if err != nil {
		return nil, errors.WithMessage(err, "failed to compose api url")
	}
	return &client{
		BackendServiceClient: NewBackendServiceClient(backendConn),
		auth:                 auth,
		baseURL:              baseURL,
		httpCli:              &http.Client{Transport: auth},
	}, nil
}

func (c *client) GetResourceStream(
	ctx context.Context, resourceType ResourceType, key string,
) (int64, io.ReadCloser, error) {
	// URL: /resource/:type/:key
	path, err := url.JoinPath(c.baseURL, "/resource", resourceType.String(), key)
	if err != nil {
		return 0, nil, errors.WithMessage(err, "invalid resource key")
	}

	// Compose request. The underlying round-tripper will take care of authentications.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, nil, errors.WithMessagef(err, "failed to compose request")
	}

	resp, err := c.httpCli.Do(req)
	if err != nil {
		return 0, nil, errors.WithMessagef(err, "failed to get resource")
	}

	defer func() {
		// Drain the body if an error occurred.
		if err != nil {
			_ = resp.Body.Close()
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, nil, errors.New("a non-2xx code is received from the backend")
	}

	return resp.ContentLength, resp.Body, nil
}

type putResourceStreamResponse struct {
	Reason *string `json:"reason,omitempty"`
	Key    *string `json:"key,omitempty"`
}

func (c *client) PutResourceStream(
	ctx context.Context, resourceType ResourceType, size int64, body io.ReadCloser,
) (string, error) {
	path, err := url.JoinPath(c.baseURL, "/resource", resourceType.String())
	if err != nil {
		return "", errors.WithMessage(err, "invalid resource key")
	}

	// Compose request. The underlying round-tripper will take care of authentications.
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, path, body)
	if err != nil {
		return "", errors.WithMessagef(err, "failed to compose request")
	}
	req.ContentLength = size

	resp, err := c.httpCli.Do(req)
	if err != nil {
		return "", errors.WithMessagef(err, "failed to get resource")
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errors.WithMessagef(err, "failed to read the response body")
	}

	var respBody putResourceStreamResponse
	if err = json.Unmarshal(rawBody, &respBody); err != nil {
		return "", errors.WithMessagef(err, "faile to unmarshal the response body, raw: %v", rawBody)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.Errorf(
			"a non-2xx code is received from the backend, reason: %s", lo.FromPtr(respBody.Reason),
		)
	}

	if respBody.Key == nil {
		return "", errors.New("the backend did accept the resource, but the resource key is missing")
	}

	return *respBody.Key, nil
}

func (c *client) ReportExit(ctx context.Context, reason string) {
	c.auth.gracefulExit(ctx, reason)
}
