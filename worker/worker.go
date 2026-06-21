package worker

import "time"

type State string

const (
	StateIdle  State = "idle"
	StateBusy  State = "busy"
	StateError State = "error"
)

type Job struct {
	Language string
	Code     string
	Result   chan Result
}

type Result struct {
	Output        string
	Success       bool
	Error         error
	ExecutionTime time.Duration
}

type Info struct {
	ID    string
	State State
}

type Config struct {
	Image       string
	MaxWorkers  int
	MemoryLimit int64
	CPULimit    int64
}

// WorkerPool is the interface for code execution backends (docker, firecracker, etc.)
type WorkerPool interface {
	Initialize() error
	ExecuteJob(language, code string) (Result, error)
	Shutdown() error
	Stats() PoolStats
}

type PoolStats struct {
	ActiveWorkers int
	QueuedJobs    int
	TotalExecuted int64
}
