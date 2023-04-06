package config

import (
	"io/fs"
	"os"
	"path/filepath"
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

type LanguageConfEntity struct {
	SourceName        string            `yaml:"SourceName"`
	ArtifactName      string            `yaml:"ArtifactName"`
	NoCompilation     bool              `default:"false" yaml:"NoCompilation"`
	Compiler          string            `yaml:"Compiler"`
	CompileCmd        string            `yaml:"CompileCmd"`
	CompileTimeLimit  uint32            `default:"20000" yaml:"CompileTimeLimit"`
	CompileSpaceLimit uint64            `default:"134217728" yaml:"CompileSpaceLimit"`
	ExecuteCmd        string            `yaml:"ExecuteCmd"`
	Environment       []string          `yaml:"Environment"`
	ResourceFactor    uint64            `yaml:"ResourceFactor"`
	Options           map[string]string `yaml:"Options"`
}

type LanguageConf map[string]LanguageConfEntity

type Config struct {
	Level   string `default:"dev" env:"RELEASE" validate:"oneof=production dev debug"` // `production`, `dev`, `debug`
	Silent  bool   `default:"false" env:"SILENT"`
	Storage string `default:"http" env:"STORAGE" validate:"oneof=grpc http s3"`
	OSS     struct {
		Endpoint        string `default:"localhost" env:"OSS_ENDPOINT"`
		AccessKeyID     string `default:"" env:"OSS_ACCESS_KEY_ID"`
		SecretAccessKey string `default:"" env:"OSS_SECRET_ACCESS_KEY"`
		BucketName      string `default:"" env:"OSS_BUCKET_NAME"`
		UseTLS          bool   `default:"false" env:"OSS_USE_TLS"`
		Region          string `default:"" env:"OSS_REGION"`
	}
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
		MaxTruncatedOutput uint     `default:"10240"`
		Environment        []string `default:"[\"PATH=/usr/bin:/bin\"]"`
	}
	LanguageConf LanguageConf
}

func LoadConf(confPaths ...string) (*Config, error) {
	configs, err := collectConfigs(confPaths)
	if err != nil {
		return nil, err
	}
	conf := new(Config)
	if err = configor.New(&configor.Config{
		Debug:                strings.ToLower(os.Getenv("ENV")) == "debug",
		ErrorOnUnmatchedKeys: true,
	}).Load(conf, configs...); err != nil {
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

func collectConfigs(confPaths []string) ([]string, error) {
	var configs []string
	for _, conf := range confPaths {
		f, err := os.Open(conf)
		if err != nil {
			return nil, errors.Wrapf(err, "cannot open file: %s", conf)
		}
		stat, err := f.Stat()
		if err != nil {
			return nil, errors.Wrapf(err, "cannot read file: %s", conf)
		}

		if !stat.IsDir() {
			configs = append(configs, conf)
			continue
		}

		entries, err := fs.ReadDir(os.DirFS(conf), ".")
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list configs under %s", conf)
		}
		for _, ent := range entries {
			if ent.IsDir() {
				continue
			}
			configs = append(configs, filepath.Join(conf, ent.Name()))
		}
	}
	return configs, nil
}
