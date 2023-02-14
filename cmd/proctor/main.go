package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/criyle/go-judge/filestore"
	"github.com/criyle/go-judge/worker"
	"github.com/samber/lo"
	"go.uber.org/multierr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"proctor-signal/config"
	"proctor-signal/external/backend"
	"proctor-signal/external/gojudge"
	"proctor-signal/judge"
	"proctor-signal/resource"
	judgeworker "proctor-signal/worker"
)

func main() {
	conf := lo.Must(config.LoadConf("conf"))
	logger := initLogger(conf)
	defer func() { _ = logger.Sync() }()

	sugar := logger.Sugar()
	sugar.Debugf("conf: %+v", conf)

	ctx, cancel := context.WithCancel(context.Background())

	logger.Info("connecting to the backend...")
	backendCli, err := connectBackend(ctx, logger, conf, cancel)
	if err != nil {
		logger.Sugar().Fatalf("failed to connect to the backend after retries, %+v", err)
	}
	logger.Info("Successfully connected to the backend")

	// Init FileStore
	judgeConf := conf.GoJudgeConf
	fs, fsCleanUp := newFileStore(logger, judgeConf)

	// Init go-judge.
	work := gojudge.NewGoJudge(logger, judgeConf, fs)
	work.Start()
	logger.Sugar().Infof("Started worker, concurrency=%d, workdir=%s, timeLimitCheckInterval=%v",
		judgeConf.Parallelism, judgeConf.Dir, judgeConf.TimeLimitCheckerInterval)

	// Init executor.
	resManager := resource.NewResourceManager(logger, backendCli, fs.(*resource.FileStore))
	judgeManager := lo.Must(judge.NewJudgeManager(work, conf, fs.(*resource.FileStore), logger))
	w := judgeworker.NewWorker(judgeManager, resManager, backendCli)
	w.Start(ctx, logger, judgeConf.Parallelism)

	// Graceful shutdown...
	sig := make(chan os.Signal, 3)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
		signal.Reset(os.Interrupt)
	case <-ctx.Done():
	}

	sugar.Info("Shutting Down...")
	cancel()

	shutdown(sugar, backendCli, work, fsCleanUp)
}

func shutdown(sugar *zap.SugaredLogger, cli backend.Client, work worker.Worker, fsCleanUp func() error) {
	ctx, cancel := context.WithTimeout(context.TODO(), time.Second*3)
	defer cancel()

	var err error
	if err = cli.ReportExit(ctx, "exiting"); err != nil {
		sugar.Warnf("failed to exit gracefully, %+v", err)
	}
	sugar.Info("disconnected from the backend")

	work.Shutdown()
	err = multierr.Append(err, fsCleanUp())

	go func() {
		sugar.Info("Shutdown Finished, ", err)
		cancel()
	}()
	<-ctx.Done()
}

func connectBackend(
	ctx context.Context, logger *zap.Logger, conf *config.Config, cancel context.CancelFunc,
) (cli backend.Client, err error) {
	err = backoff.Retry(func() error {
		var err error
		cli, err = backend.NewBackendClient(ctx, logger, conf, cancel)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 8))
	return
}

func initLogger(conf *config.Config) (logger *zap.Logger) {
	if conf.Silent {
		return zap.NewNop()
	}

	zapConfig := zap.NewDevelopmentConfig()
	switch conf.Level {
	case "production":
		zapConfig = zap.NewProductionConfig()
	case "dev":
		zapConfig.Level.SetLevel(zap.InfoLevel)
		fallthrough
	case "debug":
		zapConfig.DisableStacktrace = true
		zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	var err error
	if logger, err = zapConfig.Build(); err != nil {
		log.Fatalf("failed to initialize logger: %+v\n", err)
	}
	return
}

func newFileStore(logger *zap.Logger, conf *config.JudgeConfig) (filestore.FileStore, func() error) {
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
		sugar.Fatalf("failed to prepare storeFS directory %s, %s", conf.Dir, err)
	}

	fs, err = resource.NewFileStore(logger, conf.Dir)
	if err != nil {
		sugar.Fatal("failed to initialize the file store")
	}

	if conf.FileTimeout > 0 {
		fs = filestore.NewTimeout(fs, conf.FileTimeout, timeoutCheckInterval)
	}
	return fs, cleanUp
}
