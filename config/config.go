package config

import (
	"os"
	"strings"

	judgeconfig "github.com/criyle/go-judge/cmd/executorserver/config"
	"github.com/go-playground/validator/v10"
	"github.com/jinzhu/configor"
	"github.com/pkg/errors"
)

var (
	Version string
)

type JudgeConfig judgeconfig.Config

type LanguageConf map[string]struct {
	SourceName        string            `yaml:"SourceName"`
	ArtifactName      string            `yaml:"ArtifactName"`
	CompileCmd        string            `yaml:"CompileCmd"`
	CompileTimeLimit  uint32            `yaml:"CompileTimeLimit"`
	CompileSpaceLimit uint64            `yaml:"CompileSpaceLimit"`
	ExecuteCmd        string            `yaml:"ExecuteCmd"`
	Options           map[string]string `yaml:"Options"`
}

type Config struct {
	Level   string `default:"dev" env:"RELEASE" validate:"oneof=production dev debug"` // `production`, `dev`, `debug`
	Silent  bool   `default:"false" env:"SILENT"`
	Backend struct {
		Addr         string `default:"localhost" env:"BACKEND_ADDR"`
		GrpcPort     uint   `default:"9003" env:"BACKEND_GRPC_PORT"`
		HttpPort     uint   `default:"8080" env:"BACKEND_HTTP_PORT"`
		HttpPrefix   string `default:"/" env:"BACKEND_HTTP_PREFIX"`
		HttpTLS      bool   `default:"false" env:"BACKEND_HTTP_TLS"`
		InsecureGrpc bool   `default:"false" env:"BACKEND_INSECURE_GRPC"`
		InsecureJwt  bool   `default:"false" env:"BACKEND_INSECURE_JWT"`
		AuthSecret   string `required:"true" env:"BACKEND_AUTH_SECRET"`
		JwtPubKey    string `env:"BACKEND_JWT_PUB_KEY"`
	}
	GoJudgeConf  *JudgeConfig
	JudgeOptions struct {
		MaxTruncatedOutput uint `default:"10240"`
	}
	LanguageConf LanguageConf
}

func LoadConf(confPaths ...string) (*Config, error) {
	conf := new(Config)
	if err := configor.New(&configor.Config{
		Debug:                strings.ToLower(os.Getenv("ENV")) == "debug",
		ErrorOnUnmatchedKeys: true,
	}).Load(conf, confPaths...); err != nil {
		return nil, err
	}

	// TODO: refactor go-judge config.
	if conf.GoJudgeConf == nil {
		judgeConf := new(judgeconfig.Config)
		if err := judgeConf.Load(); err != nil {
			return nil, err
		}
		conf.GoJudgeConf = (*JudgeConfig)(judgeConf)
	}

	validate := validator.New()
	if err := validate.Struct(conf); err != nil {
		return nil, errors.WithMessage(err, "failed to validate configurations")
	}

	return conf, nil
}
