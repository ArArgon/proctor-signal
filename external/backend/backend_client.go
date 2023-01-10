package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"

	"github.com/pkg/errors"
	"github.com/samber/lo"
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
}

type client struct {
	BackendServiceClient
	httpCli *http.Client
	apiURL  string
}

// NewBackendClient builds a backend.Client with given configurations.
func NewBackendClient() (Client, error) {
	// TODO(ArArgon)
	return &client{}, nil
}

func (c *client) GetResourceStream(
	ctx context.Context, resourceType ResourceType, key string,
) (int64, io.ReadCloser, error) {
	// URL: /resource/:type/:key
	path, err := url.JoinPath(c.apiURL, "/resource", resourceType.String(), key)
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
	path, err := url.JoinPath(c.apiURL, "/resource", resourceType.String())
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
