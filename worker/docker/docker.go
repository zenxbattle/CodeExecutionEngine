package docker

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"zenxbattle/worker"
)

type DockerPool struct {
	cfg      worker.Config
	jobs     chan worker.Job
	stats    worker.PoolStats
	mu       sync.Mutex
	shutdown chan struct{}
	wg       sync.WaitGroup
}

func NewPool(cfg worker.Config) (*DockerPool, error) {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 3
	}
	return &DockerPool{
		cfg:      cfg,
		jobs:     make(chan worker.Job, cfg.MaxWorkers*4),
		shutdown: make(chan struct{}),
	}, nil
}

func (p *DockerPool) Initialize() error {
	for i := 0; i < p.cfg.MaxWorkers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	return nil
}

func (p *DockerPool) ExecuteJob(language, code string) (worker.Result, error) {
	result := make(chan worker.Result, 1)
	select {
	case p.jobs <- worker.Job{Language: language, Code: code, Result: result}:
		atomic.AddInt64(&p.stats.TotalExecuted, 1)
		r := <-result
		return r, r.Error
	default:
		return worker.Result{}, fmt.Errorf("job queue full")
	}
}

func (p *DockerPool) Shutdown() error {
	close(p.shutdown)
	p.wg.Wait()
	return nil
}

func (p *DockerPool) Stats() worker.PoolStats {
	p.stats.ActiveWorkers = p.cfg.MaxWorkers
	p.stats.QueuedJobs = len(p.jobs)
	return p.stats
}

func (p *DockerPool) worker(id int) {
	defer p.wg.Done()
	for {
		select {
		case job := <-p.jobs:
			result := p.executeCode(job.Language, job.Code)
			job.Result <- result
		case <-p.shutdown:
			return
		}
	}
}

func (p *DockerPool) executeCode(language, code string) worker.Result {
	cfg, ok := getLanguageConfig(language)
	if !ok {
		return worker.Result{Error: fmt.Errorf("unsupported language: %s", language)}
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	start := time.Now()
	var output bytes.Buffer

	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-i",
		"--network=none",
		"--memory=400m",
		"--cpus=0.5",
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges",
		p.cfg.Image,
		"sh", "-c", cfg.Command,
	)
	cmd.Stdin = strings.NewReader(code)
	cmd.Stdout = &output
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	err := cmd.Run()
	return worker.Result{
		Output:        output.String(),
		Success:       err == nil,
		Error:         fmt.Errorf("%s: %v", errBuf.String(), err),
		ExecutionTime: time.Since(start),
	}
}
