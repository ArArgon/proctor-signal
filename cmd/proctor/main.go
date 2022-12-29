package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"time"

	"proctor-signal/judge"
	"proctor-signal/resource"

	judgeconfig "github.com/criyle/go-judge/cmd/executorserver/config"
	"github.com/criyle/go-judge/env"
	"github.com/criyle/go-judge/env/pool"
	"github.com/criyle/go-judge/envexec"
	"github.com/criyle/go-judge/filestore"
	"github.com/criyle/go-judge/worker"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/errgroup"
)

var logger *zap.Logger

type stopFunc func(ctx context.Context) error
type initFunc func() (start func(), cleanUp stopFunc)

func main() {
	conf := loadConf()
	initLogger(conf)
	sugar := logger.Sugar()

	defer func() { _ = logger.Sync() }()

	sugar.Infof("conf: %+v", conf)

	// Init environment pool
	fs, fsCleanUp := newFileStore(conf)
	b := newEnvBuilder(conf)
	envPool := newEnvPool(b)
	prefork(envPool, conf.PreFork)
	work := newWorker(conf, envPool, fs)
	work.Start()
	logger.Sugar().Infof("Started worker, concurrency=%d, workdir=%s, timeLimitCheckInterval=%v",
		conf.Parallelism, conf.Dir, conf.TimeLimitCheckerInterval)

	servers := []initFunc{
		cleanUpWorker(work),
		cleanUpFs(fsCleanUp),
	}

	// Gracefully shutdown.
	sig := make(chan os.Signal, 1+len(servers))

	// worker and fs clean up func
	var stops []stopFunc
	for _, s := range servers {
		start, stop := s()
		if start != nil {
			go func() {
				start()
				sig <- os.Interrupt
			}()
		}
		if stop != nil {
			stops = append(stops, stop)
		}
	}

	// background force GC worker
	newForceGCWorker(conf)

	manager := judge.NewJudgeManager(work)
	sugar.Infof(manager.ExecuteCommand(context.Background(), "echo hello world!"))

	// Graceful shutdown...
	signal.Notify(sig, os.Interrupt)
	<-sig
	signal.Reset(os.Interrupt)

	sugar.Info("Shutting Down...")

	ctx, cancel := context.WithTimeout(context.TODO(), time.Second*3)
	defer cancel()

	var eg errgroup.Group
	for _, s := range stops {
		s := s
		eg.Go(func() error {
			return s(ctx)
		})
	}

	go func() {
		logger.Sugar().Info("Shutdown Finished ", eg.Wait())
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

func newEnvBuilder(conf *judgeconfig.Config) pool.EnvBuilder {
	b, err := env.NewBuilder(env.Config{
		ContainerInitPath:  conf.ContainerInitPath,
		MountConf:          conf.MountConf,
		TmpFsParam:         conf.TmpFsParam,
		NetShare:           conf.NetShare,
		CgroupPrefix:       conf.CgroupPrefix,
		Cpuset:             conf.Cpuset,
		ContainerCredStart: conf.ContainerCredStart,
		EnableCPURate:      conf.EnableCPURate,
		CPUCfsPeriod:       conf.CPUCfsPeriod,
		SeccompConf:        conf.SeccompConf,
		Logger:             logger.Sugar(),
	})
	if err != nil {
		logger.Sugar().Fatal("create environment builder failed ", err)
	}
	//if conf.EnableMetrics {
	//	b = &metricsEnvBuilder{b}
	//}
	return b
}

func newEnvPool(b pool.EnvBuilder) worker.EnvironmentPool {
	p := pool.NewPool(b)
	//if enableMetrics {
	//	p = &metricsEnvPool{p}
	//}
	return p
}

func newWorker(conf *judgeconfig.Config, envPool worker.EnvironmentPool, fs filestore.FileStore) worker.Worker {
	return worker.New(worker.Config{
		FileStore:             fs,
		EnvironmentPool:       envPool,
		Parallelism:           conf.Parallelism,
		WorkDir:               conf.Dir,
		TimeLimitTickInterval: conf.TimeLimitCheckerInterval,
		ExtraMemoryLimit:      *conf.ExtraMemoryLimit,
		OutputLimit:           *conf.OutputLimit,
		CopyOutLimit:          *conf.CopyOutLimit,
		OpenFileLimit:         uint64(conf.OpenFileLimit),
	})
}

func prefork(envPool worker.EnvironmentPool, prefork int) {
	if prefork <= 0 {
		return
	}
	logger.Sugar().Info("create ", prefork, " pre-forked containers")
	m := make([]envexec.Environment, 0, prefork)
	for i := 0; i < prefork; i++ {
		e, err := envPool.Get()
		if err != nil {
			log.Fatalln("reserve pre-fork containers failed ", err)
		}
		m = append(m, e)
	}
	for _, e := range m {
		envPool.Put(e)
	}
}

func newForceGCWorker(conf *judgeconfig.Config) {
	go func() {
		ticker := time.NewTicker(conf.ForceGCInterval)
		for {
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			if mem.HeapInuse > uint64(*conf.ForceGCTarget) {
				logger.Sugar().Infof("Force GC as heap_in_use(%v) > target(%v)",
					envexec.Size(mem.HeapInuse), *conf.ForceGCTarget)
				runtime.GC()
				debug.FreeOSMemory()
			}
			<-ticker.C
		}
	}()
}

func cleanUpWorker(work worker.Worker) initFunc {
	return func() (start func(), cleanUp stopFunc) {
		return nil, func(ctx context.Context) error {
			work.Shutdown()
			logger.Sugar().Info("Worker shutdown")
			return nil
		}
	}
}

func cleanUpFs(fsCleanUp func() error) initFunc {
	return func() (start func(), cleanUp stopFunc) {
		if fsCleanUp == nil {
			return nil, nil
		}
		return nil, func(ctx context.Context) error {
			err := fsCleanUp()
			logger.Sugar().Info("FileStore cleaned up")
			return err
		}
	}
}
