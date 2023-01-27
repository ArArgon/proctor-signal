package backend

import (
	"context"
	"crypto/dsa"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/golang-jwt/jwt/v4"
	"github.com/pkg/errors"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"proctor-signal/config"
	"proctor-signal/utils"
)

const defaultRefreshTime = time.Minute * 5
const defaultMaxRetryTimes = 5

type jwtClaim struct {
	jwt.RegisteredClaims
	InstanceID string `json:"instance_id,omitempty"`
}

type authManager struct {
	cli             AuthServiceClient
	logger          *zap.Logger
	version         string
	conf            *config.Config
	serverPublicKey any

	transport *http.Transport
	mut       *sync.RWMutex

	accessToken  string
	expireTime   time.Time
	instanceID   string
	noExpiration bool
}

func newAuthManager(
	ctx context.Context, logger *zap.Logger, conn *grpc.ClientConn, conf *config.Config,
) (*authManager, error) {
	m := &authManager{
		cli:     NewAuthServiceClient(conn),
		logger:  logger,
		version: config.Version,
		conf:    conf,

		transport: new(http.Transport),
		mut:       new(sync.RWMutex),
	}

	if !conf.Backend.InsecureJwt {
		if err := m.parsePublicKey(conf.Backend.JwtPubKey); err != nil {
			return nil, errors.WithMessagef(err, "failed to parse public key")
		}
	}

	if err := m.login(ctx); err != nil {
		return nil, errors.WithMessagef(err, "failed to login to the auth server")
	}

	go m.scheduleRefresh(ctx)
	return m, nil
}

func (a *authManager) scheduleRefresh(ctx context.Context) {
	if a.noExpiration {
		return
	}

	tick := time.NewTicker(defaultRefreshTime)
	defer tick.Stop()
	for {
		sugar := a.logger.Sugar().With("module", "tokenRefresh")
		select {
		case <-ctx.Done():
			return
		case t := <-tick.C:
			// Only renew the token less than 2 * defaultRefreshTime before expiration.
			if a.expireTime.Sub(t) > defaultRefreshTime*2 {
				continue
			}

			sugar.Info("refreshing token")
			err := backoff.Retry(
				func() error { return a.refresh(ctx) },
				backoff.WithContext(backoff.WithMaxRetries(backoff.NewExponentialBackOff(), defaultMaxRetryTimes), ctx),
			)

			if err != nil {
				sugar.With("err", err).Errorf("failed to refresh token with 5 retries")
			}

			if a.noExpiration {
				return
			}
		}
	}
}

func (a *authManager) parsePublicKey(pubKey string) error {
	sugar := a.logger.Sugar()
	block, _ := pem.Decode([]byte(pubKey))
	if block == nil || block.Type != "PUBLIC KEY" {
		return errors.New("invalid public key")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return errors.WithMessage(err, "failed to parse public key")
	}

	switch pub := pub.(type) {
	case *rsa.PublicKey:
		sugar.Info("server public key is RSA:", pub)
	case *dsa.PublicKey:
		sugar.Info("server public key is DSA:", pub)
	case *ecdsa.PublicKey:
		sugar.Info("server public key is ECDSA:", pub)
	case ed25519.PublicKey:
		sugar.Info("server public key is Ed25519:", pub)
	default:
		return errors.Errorf("unknown type of public key: %+v", pub)
	}
	a.serverPublicKey = pub

	return nil
}

func (a *authManager) updateToken(accessToken string) error {
	a.mut.Lock()
	defer a.mut.Unlock()

	parser := jwt.NewParser()
	claim, err := parser.ParseWithClaims(accessToken, &jwtClaim{}, func(token *jwt.Token) (interface{}, error) {
		claim := token.Claims.(*jwtClaim)
		if a.instanceID != "" && a.instanceID != claim.InstanceID {
			return nil, errors.Errorf(
				"unmatched instanceID, expecting %s, got: %s", a.instanceID, claim.InstanceID,
			)
		}
		return lo.Ternary(a.conf.Backend.InsecureJwt, nil, a.serverPublicKey), nil
	})

	if err != nil && (!a.conf.Backend.InsecureJwt && !errors.Is(err, jwt.ErrTokenSignatureInvalid)) {
		return err
	}

	// Update expiration time & instance id.
	c := claim.Claims.(*jwtClaim)
	a.instanceID = c.InstanceID
	a.noExpiration = c.ExpiresAt == nil || c.ExpiresAt.IsZero()
	a.expireTime = lo.Ternary(a.noExpiration, c.ExpiresAt.Time, time.Time{})
	a.accessToken = accessToken

	return nil
}

func (a *authManager) login(ctx context.Context) error {
	resp, err := a.cli.Register(ctx, &RegisterRequest{
		Secret:    a.conf.Backend.AuthSecret,
		Version:   a.version,
		IpAddress: utils.GetLocalIP(),
	})
	if err != nil {
		return err
	}

	if resp.StatusCode >= 300 || resp.StatusCode < 200 {
		// Server-side err
		return errors.Errorf("failed to login, server-side err: %v", resp.GetReason())
	}

	newToken := resp.GetToken()
	if newToken == "" {
		return errors.New("got an empty token from server")
	}
	return a.updateToken(newToken)
}

func (a *authManager) refresh(ctx context.Context) error {
	resp, err := a.cli.RenewToken(ctx, &RenewTokenRequest{
		Secret:     a.conf.Backend.AuthSecret,
		Version:    a.version,
		Token:      a.accessToken,
		InstanceId: a.instanceID,
	})
	if err != nil {
		// Failed to refresh token.
		return err
	}
	if resp.StatusCode >= 300 || resp.StatusCode < 200 {
		// Server-side err
		return errors.Errorf("failed to refresh token, server-side err: %v", resp.GetReason())
	}

	newToken := resp.GetToken()
	if newToken == "" {
		return errors.New("got an empty token from server")
	}
	return a.updateToken(newToken)
}

func (a *authManager) attachToken(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "Token", a.accessToken)
}

func (a *authManager) unaryInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req, reply interface{},
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
		a.mut.RLock()
		ctx = a.attachToken(ctx)
		a.mut.RUnlock()
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func (a *authManager) streamInterceptor() grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		a.mut.RLock()
		ctx = a.attachToken(ctx)
		a.mut.RUnlock()
		return streamer(ctx, desc, cc, method, opts...)
	}
}

func (a *authManager) RoundTrip(request *http.Request) (*http.Response, error) {
	// Append bearer's token.
	a.mut.RLock()
	request.Header.Set("Authorization", "Bearer "+a.accessToken)
	a.mut.RUnlock()
	return a.transport.RoundTrip(request)
}
