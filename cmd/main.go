package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"zenxbattle/config"
	"zenxbattle/natsclient"
	"zenxbattle/natshandler"
	"zenxbattle/worker"
	workerdocker "zenxbattle/worker/docker"
	workerfc "zenxbattle/worker/firecracker"
)

func main() {
	cfg := config.LoadConfig()
	log.Printf("Loaded config: %+v", cfg)

	wCfg := worker.Config{
		Image:       cfg.WorkerImage,
		MaxWorkers:  cfg.MaxWorkers,
		MemoryLimit: cfg.WorkerMemLimit,
		CPULimit:    cfg.WorkerCPULimit,
	}

	if cfg.AutoSpawn {
		wCfg.MaxWorkers = config.CalcAutoMaxWorkers(2000, 2048, cfg.WorkerCPU, cfg.WorkerMem)
		log.Printf("autospawn: calculated maxWorkers=%d", wCfg.MaxWorkers)
	}

	var pool worker.WorkerPool
	var err error

	switch cfg.WorkerType {
	case "firecracker":
		pool, err = workerfc.NewPool(wCfg)
		log.Println("Using Firecracker worker")
	default:
		pool, err = workerdocker.NewPool(wCfg)
		log.Println("Using Docker worker")
	}

	if err != nil {
		log.Fatalf("Failed to create worker pool: %v", err)
	}
	if err := pool.Initialize(); err != nil {
		log.Fatalf("Failed to initialize: %v", err)
	}
	log.Println("Worker pool initialized")

	nc, err := natsclient.Connect(cfg.NatsURL)
	if err != nil {
		log.Fatalf("Failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	natshandler.StartHandlers(nc, pool, cfg.ShowOutput)

	// Health endpoint
	go func() {
		// TODO: HTTP health server on cfg.Port
	}()

	log.Printf("Engine ready [type=%s workers=%d]", cfg.WorkerType, wCfg.MaxWorkers)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("Shutting down...")
	pool.Shutdown()
}
