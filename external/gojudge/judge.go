package gojudge

import (
	"fmt"
	"log"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/criyle/go-judge/cmd/executorserver/version"
	"github.com/criyle/go-judge/env"
	"github.com/criyle/go-judge/env/pool"
	"github.com/criyle/go-judge/envexec"
	"github.com/criyle/go-judge/filestore"
	"github.com/criyle/go-judge/worker"
	"go.uber.org/zap"

	"proctor-signal/config"
)

var logger *zap.Logger

func Init(l *zap.Logger, conf *config.JudgeConfig) {
	logger = l
	if conf.Version {
		fmt.Print(version.Version)
		return
	}
	warnIfNotLinux()
}

func warnIfNotLinux() {
	if runtime.GOOS != "linux" {
		logger.Sugar().Warn("Platform is ", runtime.GOOS)
		logger.Sugar().Warn("Please notice that the primary supporting platform is Linux")
		logger.Sugar().Warn("Windows and macOS(darwin) support are only recommended in development environment")
	}
}

// func initLogger(conf *config.Config) {
// 	if conf.Silent {
// 		logger = zap.NewNop()
// 		return
// 	}

// 	var err error
// 	if conf.Release {
// 		logger, err = zap.NewProduction()
// 	} else {
// 		config := zap.NewDevelopmentConfig()
// 		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
// 		if !conf.EnableDebug {
// 			config.Level.SetLevel(zap.InfoLevel)
// 		}
// 		logger, err = config.Build()
// 	}
// 	if err != nil {
// 		log.Fatalln("init logger failed ", err)
// 	}
// }

func Prefork(envPool worker.EnvironmentPool, prefork int) {
	if prefork <= 0 {
		return
	}
	logger.Sugar().Info("create ", prefork, " prefork containers")
	m := make([]envexec.Environment, 0, prefork)
	for i := 0; i < prefork; i++ {
		e, err := envPool.Get()
		if err != nil {
			log.Fatalln("prefork environment failed ", err)
		}
		m = append(m, e)
	}
	for _, e := range m {
		envPool.Put(e)
	}
}

func NewEnvBuilder(conf *config.JudgeConfig) pool.EnvBuilder {
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
	if conf.EnableMetrics {
		b = &metriceEnvBuilder{b}
	}
	return b
}

func NewEnvPool(b pool.EnvBuilder, enableMetrics bool) worker.EnvironmentPool {
	p := pool.NewPool(b)
	if enableMetrics {
		p = &metricsEnvPool{p}
	}
	return p
}

func NewWorker(conf *config.JudgeConfig, envPool worker.EnvironmentPool, fs filestore.FileStore) worker.Worker {
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
		ExecObserver:          execObserve,
	})
}

func NewForceGCWorker(conf *config.JudgeConfig) {
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

func CleanUpWorker(work worker.Worker) {
	work.Shutdown()
	logger.Sugar().Info("Worker shutdown")
}
