package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	"proctor-signal/external/gojudge"
	"proctor-signal/judge"
	"proctor-signal/resource"

	judgeconfig "github.com/criyle/go-judge/cmd/executorserver/config"
	"github.com/criyle/go-judge/filestore"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var logger *zap.Logger

func main() {
	conf := loadConf()
	initLogger(conf)
	sugar := logger.Sugar()

	defer func() { _ = logger.Sync() }()

	sugar.Infof("conf: %+v", conf)

	// Init gojudge
	fs, fsCleanUp := newFileStore(conf)
	b := gojudge.NewEnvBuilder(conf)
	envPool := gojudge.NewEnvPool(b, false)
	gojudge.Prefork(envPool, conf.PreFork)
	work := gojudge.NewWorker(conf, envPool, fs)
	work.Start()
	logger.Sugar().Infof("Started worker, concurrency=%d, workdir=%s, timeLimitCheckInterval=%v",
		conf.Parallelism, conf.Dir, conf.TimeLimitCheckerInterval)

	// background force GC worker
	gojudge.NewForceGCWorker(conf)

	manager := judge.NewJudgeManager(work)
	sugar.Infof(manager.ExecuteCommand(context.Background(), "echo hello world!"))

	// Graceful shutdown...
	sig := make(chan os.Signal, 3)
	signal.Notify(sig, os.Interrupt)
	<-sig
	signal.Reset(os.Interrupt)

	sugar.Info("Shutting Down...")

	ctx, cancel := context.WithTimeout(context.TODO(), time.Second*3)
	defer cancel()

	gojudge.CleanUpWorker(work)
	err := fsCleanUp()

	go func() {
		logger.Sugar().Info("Shutdown Finished ", err)
		cancel()
	}()
	<-ctx.Done()
}

func loadConf() *judgeconfig.Config {
	var conf judgeconfig.Config
	if err := conf.Load(); err != nil {
		if err == flag.ErrHelp {
			os.Exit(0)
		}
		log.Fatalln("load config failed ", err)
	}
	return &conf
}

func initLogger(conf *judgeconfig.Config) {
	if conf.Silent {
		logger = zap.NewNop()
		return
	}

	var err error
	if conf.Release {
		logger, err = zap.NewProduction()
	} else {
		config := zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		if !conf.EnableDebug {
			config.Level.SetLevel(zap.InfoLevel)
		}
		logger, err = config.Build()
	}
	if err != nil {
		log.Fatalln("failed to initialize logger: ", err)
	}
}

func newFileStore(conf *judgeconfig.Config) (filestore.FileStore, func() error) {
	const timeoutCheckInterval = 15 * time.Second
	sugar := logger.Sugar()
	var (
		cleanUp func() error
		err     error
	)

	var fs filestore.FileStore
	if conf.Dir == "" {
		conf.Dir, err = os.MkdirTemp(os.TempDir(), "signal")
		if err != nil {
			sugar.Fatal("failed to create file store temp dir", err)
		}
		cleanUp = func() error {
			return os.RemoveAll(conf.Dir)
		}
	}
	if err = os.MkdirAll(conf.Dir, 0755); err != nil {
		sugar.With("err", err).Fatal("failed to prepare storeFS directory: ", conf.Dir)
	}

	fs, err = resource.NewFileStore(logger, conf.Dir)
	if err != nil {
		sugar.Fatal("failed to initialize the file store")
	}
	//if conf.EnableDebug {
	//	fs = newMetricsFileStore(fs)
	//}
	if conf.FileTimeout > 0 {
		fs = filestore.NewTimeout(fs, conf.FileTimeout, timeoutCheckInterval)
	}
	return fs, cleanUp
}
