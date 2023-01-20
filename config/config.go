package config

import (
	"os"
	"strings"

	judgeconfig "github.com/criyle/go-judge/cmd/executorserver/config"
	"github.com/jinzhu/configor"
)

type Config struct {
	Backend struct {
		Addr     string `default:"localhost" env:"BACKEND_ADDR"`
		GrpcPort uint   `default:"9003" env:"BACKEND_GRPC_PORT"`
		HttpPort uint   `default:"8080" env:"BACKEND_HTTP_PORT"`
		HttpTLS  bool   `default:"false" env:"BACKEND_HTTP_TLS"`
	}
	GoJudgeConf  *judgeconfig.Config
	LanguageConf []struct {
		// TODO: language configurations.
	}
}

func LoadConf(confPath string) (*Config, error) {
	conf := new(Config)
	if err := configor.New(&configor.Config{
		Debug:                strings.ToLower(os.Getenv("ENV")) == "debug",
		ErrorOnUnmatchedKeys: true,
	}).Load(conf, confPath); err != nil {
		return nil, err
	}

	// TODO: refactor go-judge config.
	if conf.GoJudgeConf == nil {
		judgeConf := new(judgeconfig.Config)
		if err := judgeConf.Load(); err != nil {
			return nil, err
		}
		conf.GoJudgeConf = judgeConf
	}

	return conf, nil
}
