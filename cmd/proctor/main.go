package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"time"

	judgeconfig "github.com/criyle/go-judge/cmd/executorserver/config"
	"github.com/criyle/go-judge/filestore"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"proctor-signal/config"
	"proctor-signal/external/backend"
	"proctor-signal/external/gojudge"
	"proctor-signal/judge"
	"proctor-signal/resource"
	judgeworker "proctor-signal/worker"
)

var logger *zap.Logger

func main() {
	conf := lo.Must(config.LoadConf("conf/signal.toml"))
	initLogger(conf.GoJudgeConf)
	sugar := logger.Sugar()
	ctx, cancel := context.WithCancel(context.Background())

	backendCli := lo.Must(backend.NewBackendClient(ctx, logger, conf))

	defer func() { _ = logger.Sync() }()

	sugar.Infof("conf: %+v", conf)

	// Init judge
	judge.LoadLanguageConfig("language.yaml")

	// Init gojudge
	judgeConf := conf.GoJudgeConf
	gojudge.Init(logger)
	fs, fsCleanUp := newFileStore(judgeConf)
	b := gojudge.NewEnvBuilder(judgeConf)
	envPool := gojudge.NewEnvPool(b, false)
	gojudge.Prefork(envPool, judgeConf.PreFork)
	work := gojudge.NewWorker(judgeConf, envPool, fs)
	work.Start()
	logger.Sugar().Infof("Started worker, concurrency=%d, workdir=%s, timeLimitCheckInterval=%v",
		judgeConf.Parallelism, judgeConf.Dir, judgeConf.TimeLimitCheckerInterval)

	// background force GC worker
	gojudge.NewForceGCWorker(judgeConf)

	resManager := resource.NewResourceManager(logger, backendCli, fs.(*resource.FileStore))
	judgeManager := judge.NewJudgeManager(work)
	w := judgeworker.NewWorker(judgeManager, resManager, backendCli)
	//sugar.Infof(judgeManager.ExecuteCommand(context.Background(), "echo hello world!"))
	w.Start(ctx, logger, 2)

	// Graceful shutdown...
	sig := make(chan os.Signal, 3)
	signal.Notify(sig, os.Interrupt)
	<-sig
	signal.Reset(os.Interrupt)

	sugar.Info("Shutting Down...")
	cancel()

	ctx, cancel = context.WithTimeout(context.TODO(), time.Second*3)
	defer cancel()

	if err := backendCli.ReportExit(ctx, "exiting"); err != nil {
		sugar.With("err", err).Warn("failed to exit gracefully")
	}
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
		zapConfig := zap.NewDevelopmentConfig()
		zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		if !conf.EnableDebug {
			zapConfig.Level.SetLevel(zap.InfoLevel)
		}
		logger, err = zapConfig.Build()
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
