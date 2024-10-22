package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/pkg/errors"
	"github.com/samber/lo"

	"proctor-signal/config"
)

type httpResClient struct {
	httpCli *http.Client
	baseURL string
}

func newHttpResClient(auth *authManager, conf *config.Config) (*httpResClient, error) {
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

	return &httpResClient{
		httpCli: &http.Client{Transport: auth},
		baseURL: baseURL,
	}, nil
}

func (c *httpResClient) GetResourceStream(
	ctx context.Context, resourceType ResourceType, key string,
) (int64, io.ReadCloser, error) {
	// URL: /resource/:type/:key
	path, err := url.JoinPath(c.baseURL, "/resource/", resourceType.String(), key)
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

func (c *httpResClient) PutResourceStream(
	ctx context.Context, resourceType ResourceType, size int64, body io.ReadCloser,
) (string, error) {
	if resourceType != ResourceType_OUTPUT_DATA {
		return "", errors.Errorf("unsupported resource type: %s", resourceType.String())
	}
	path, err := url.JoinPath(c.baseURL, "/resource/")
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
