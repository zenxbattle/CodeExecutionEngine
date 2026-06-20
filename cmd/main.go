package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
	"zenxbattle/config"
	"zenxbattle/executor"
	"zenxbattle/logutil"
	"zenxbattle/natsclient"
	"zenxbattle/natshandler"
)

func main() {
	log.Println("Loading engine configuration...")
	cfg := config.LoadConfig()
	log.Printf("Loaded config: %+v\n", cfg)

	var logger *zap.Logger
	var err error
	if cfg.Environment == "development" {
		logger, err = zap.NewDevelopment()
	} else {
		logger, err = zap.NewProduction()
	}
	if err != nil {
		panic("Failed to initialize Zap logger: " + err.Error())
	}
	defer logger.Sync()

	logShipper := logutil.New("code-execution-engine")
	log.Printf("Using worker image: %s", cfg.WorkerImage)

	// Autospawn: if MAX_WORKERS=0, calculate from pod resources
	if cfg.MaxWorkers == 0 {
		if cfg.AutoSpawn {
			podCPU := getEnvInt("POD_CPU_LIMIT", 2000)
			podMem := getEnvInt("POD_MEM_LIMIT", 2048)
			cfg.MaxWorkers = config.CalcAutoMaxWorkers(podCPU, podMem, cfg.WorkerCPU, cfg.WorkerMem)
			log.Printf("autospawn: calculated maxWorkers=%d", cfg.MaxWorkers)
		} else {
			cfg.MaxWorkers = 1
		}
	}

	log.Println("Starting worker pool initialization")
	workerPool, err := executor.NewWorkerPool(cfg.WorkerImage, cfg.MaxWorkers, cfg.JobCount, cfg.WorkerMemLimit, cfg.WorkerCPULimit, logShipper)
	if err != nil {
		logger.Fatal("Failed to initialize worker pool", zap.Error(err))
	}
	log.Println("Worker pool initialized successfully")

	var nc *natsclient.Client
	for {
		var err error
		nc, err = natsclient.NewClient(cfg.NatsURL)
		if err == nil {
			break
		}
		log.Printf("Failed to connect to NATS: %v, retrying...", err)
		time.Sleep(3 * time.Second)
	}
	defer nc.Close()
	log.Println("Successfully connected to NATS")

	rateLimiter := executor.NewRateLimiter(cfg.MaxRequestsPerMin, 60, cfg.BanDurationSec)
	defer rateLimiter.Stop()

	rateLimitResp := func(clientIP string) []byte {
		rem := rateLimiter.CooldownRemaining(clientIP)
		resp, _ := json.Marshal(map[string]interface{}{
			"success":       false,
			"error":         "rate_limit_exceeded",
			"status_message": fmt.Sprintf("Too many requests. Try again in %ds.", int(rem.Seconds())),
			"execution_time": "0ms",
		})
		return resp
	}

	log.Println("Subscribing to 'compiler.execute.request'")
	nc.Conn.QueueSubscribe("compiler.execute.request", "engine-workers", func(msg *nats.Msg) {
		log.Println("Received compiler.execute.request message")
		clientIP := msg.Header.Get("X-Client-IP")
		if cfg.MaxRequestsPerMin > 0 && !rateLimiter.Allow(clientIP) {
			msg.Respond(rateLimitResp(clientIP))
			return
		}
		resp := natshandler.HandleCompilerRequestBytes(msg.Data, workerPool, cfg.ShowOutput)
		msg.Respond(resp)
	})
	log.Println("Subscribing to 'problems.execute.request'")
	nc.Conn.QueueSubscribe("problems.execute.request", "engine-workers", func(msg *nats.Msg) {
		log.Println("Received problems.execute.request message")
		clientIP := msg.Header.Get("X-Client-IP")
		if cfg.MaxRequestsPerMin > 0 && !rateLimiter.Allow(clientIP) {
			msg.Respond(rateLimitResp(clientIP))
			return
		}
		resp := natshandler.HandleProblemRunRequestBytes(msg.Data, workerPool, cfg.ShowOutput)
		msg.Respond(resp)
	})

	log.Println("Engine service is up and listening for requests")

	addr := fmt.Sprintf(":%s", cfg.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Warn("Failed to start health listener", zap.String("port", cfg.Port), zap.Error(err))
	} else {
		go func() {
			log.Printf("Health listener started on %s", addr)
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				conn.Close()
			}
		}()
	}

	select {}
}

func getEnvInt(key string, defaultVal int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}
