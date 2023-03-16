package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/criyle/go-judge/filestore"
	"github.com/samber/lo"
	"github.com/tencentyun/cos-go-sdk-v5"
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
	conf := lo.Must(config.LoadConf("conf/signal.toml", "conf/language.toml"))
	initLogger(conf)
	sugar := logger.Sugar()
	ctx, cancel := context.WithCancel(context.Background())

	logger.Info("connecting to the backend...")
	var backendCli backend.Client
	err := backoff.Retry(func() error {
		var err error
		backendCli, err = backend.NewBackendClient(ctx, logger, conf, cancel)
		return err
	}, backoff.WithMaxRetries(backoff.NewExponentialBackOff(), 8))
	if err != nil {
		logger.Sugar().Fatalf("failed to connect to the backend after retries, %+v", err)
	}
	logger.Info("Successfully connected to the backend")

	defer func() { _ = logger.Sync() }()

	sugar.Debugf("conf: %+v", conf)

	// Init judge
	//judge.LoadLanguageConfig("language.yaml")

	// Init gojudge
	judgeConf := conf.GoJudgeConf
	gojudge.Init(logger, judgeConf)
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
	judgeManager := lo.Must(judge.NewJudgeManager(work, conf, fs.(*resource.FileStore), logger))
	cosClient := newCOSClient()
	w := judgeworker.NewWorker(judgeManager, resManager, backendCli, cosClient, conf)

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

	ctx, cancel = context.WithTimeout(context.TODO(), time.Second*3)
	defer cancel()

	if err = backendCli.ReportExit(ctx, "exiting"); err != nil {
		sugar.Warnf("failed to exit gracefully, %+v", err)
	}
	sugar.Info("disconnected from the backend")
	gojudge.CleanUpWorker(work)
	err = fsCleanUp()

	go func() {
		logger.Sugar().Info("Shutdown Finished, ", err)
		cancel()
	}()
	<-ctx.Done()
}

func initLogger(conf *config.Config) {
	if conf.Silent {
		logger = zap.NewNop()
		return
	}

	var err error
	if conf.Level == "production" {
		logger, err = zap.NewProduction()
	} else {
		zapConfig := zap.NewDevelopmentConfig()
		zapConfig.DisableStacktrace = true
		zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		zapConfig.Level.SetLevel(lo.Ternary(conf.Level == "debug", zap.DebugLevel, zap.InfoLevel))
		logger, err = zapConfig.Build()
	}
	if err != nil {
		log.Fatalf("failed to initialize logger: %+v\n", err)
	}
}

func newFileStore(conf *config.JudgeConfig) (filestore.FileStore, func() error) {
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
	//if conf.EnableDebug {
	//	fs = newMetricsFileStore(fs)
	//}
	if conf.FileTimeout > 0 {
		fs = filestore.NewTimeout(fs, conf.FileTimeout, timeoutCheckInterval)
	}
	return fs, cleanUp
}

// TODO: new COS client from conf
func newCOSClient() *cos.Client {
	u, err := url.Parse("https://hjj-1302703862.cos.ap-chengdu.myqcloud.com")
	if err != nil {
		log.Fatalf("failed to init COS client, err: %+v\n", err)
	}

	return cos.NewClient(&cos.BaseURL{BucketURL: u}, &http.Client{
		Transport: &cos.AuthorizationTransport{
			SecretID:  "AKIDtmaIrIZfZIrGTXQ9jB5pPpCZqwtfDbpP",
			SecretKey: "TnyLXYTWUOBoLm6OXXN7bxCHF7whLuNk",
		},
	})
}
