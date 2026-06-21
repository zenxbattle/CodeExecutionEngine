package config

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	WorkerType   string // "docker" or "firecracker"
	MaxWorkers   int
	AutoSpawn    bool
	JobCount     int
	WorkerImage  string
	NatsURL      string
	Environment  string
	WorkerCPU    int
	WorkerMem    int
	WorkerMemLimit int64
	WorkerCPULimit int64
	Port         string
	ShowOutput   bool
	MaxRequestsPerMin int
	BanDurationSec    int
}

func LoadConfig() Config {
	err := godotenv.Load(".env")
	if err != nil {
		log.Printf("Warning: Error loading .env file: %v", err)
	}

	return Config{
		WorkerType:       getEnv("WORKER_TYPE", "docker"),
		MaxWorkers:       getEnvInt("MAX_WORKERS", 0),
		AutoSpawn:        getEnvBool("AUTO_SPAWN", false),
		JobCount:         getEnvInt("MAX_JOBS", 3),
		WorkerImage:      getEnv("WORKER_IMAGE", "lijuthomas/zenxbattle-engine-worker:latest"),
		NatsURL:          getEnv("NATSURL", "nats://localhost:4222"),
		Environment:      getEnv("ENVIRONMENT", "production"),
		WorkerCPU:        getEnvInt("WORKER_CPU", 500),
		WorkerMem:        getEnvInt("WORKER_MEM", 400),
		WorkerMemLimit:   int64(getEnvInt("WORKER_MEM_LIMIT", 400)),
		WorkerCPULimit:   int64(getEnvInt("WORKER_CPU_LIMIT", 500)),
		Port:             getEnv("PORT", "50053"),
		ShowOutput:       getEnvBool("SHOW_OUTPUT", false),
		MaxRequestsPerMin: getEnvInt("MAX_REQUESTS_PER_MIN", 10),
		BanDurationSec:   getEnvInt("BAN_DURATION_SEC", 60),
	}
}

func CalcAutoMaxWorkers(podCPU, podMem, workerCPU, workerMem int) int {
	byCPU := int(float64(podCPU)*0.8/float64(workerCPU))
	byMem := int(float64(podMem)*0.8/float64(workerMem))
	if m := min(byCPU, byMem); m > 0 {
		return m
	}
	return 1
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists { return value }
	return defaultValue
}
func getEnvInt(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		if intVal, err := strconv.Atoi(value); err == nil { return intVal }
	}
	return defaultValue
}
func getEnvBool(key string, defaultVal bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok { return defaultVal }
	return v == "true" || v == "1"
}
