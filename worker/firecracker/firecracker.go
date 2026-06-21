package firecracker

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"zenxbattle/worker"
)

type FCPool struct {
	cfg      worker.Config
	stats    worker.PoolStats
	mu       sync.Mutex
	jobs     chan worker.Job
	shutdown chan struct{}
	wg       sync.WaitGroup
}

func NewPool(cfg worker.Config) (*FCPool, error) {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 2 // microVMs are heavier
	}
	return &FCPool{
		cfg:      cfg,
		jobs:     make(chan worker.Job, cfg.MaxWorkers*4),
		shutdown: make(chan struct{}),
	}, nil
}

func (p *FCPool) Initialize() error {
	for i := 0; i < p.cfg.MaxWorkers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	return nil
}

func (p *FCPool) ExecuteJob(language, code string) (worker.Result, error) {
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

func (p *FCPool) Shutdown() error {
	close(p.shutdown)
	p.wg.Wait()
	return nil
}

func (p *FCPool) Stats() worker.PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stats.ActiveWorkers = p.cfg.MaxWorkers
	p.stats.QueuedJobs = len(p.jobs)
	return p.stats
}

func (p *FCPool) worker(id int) {
	defer p.wg.Done()
	for {
		select {
		case job := <-p.jobs:
			result := p.executeMicroVM(job.Language, job.Code)
			job.Result <- result
		case <-p.shutdown:
			return
		}
	}
}

func (p *FCPool) executeMicroVM(language, code string) worker.Result {
	// TODO: Implement Firecracker microVM execution
	// Steps:
	// 1. Create rootfs overlay from base image
	// 2. Write code into rootfs
	// 3. Boot microVM with firecracker
	// 4. Capture stdout
	// 5. Kill microVM
	// 6. Cleanup overlay
	return worker.Result{
		Output:  fmt.Sprintf("Firecracker worker: %s execution queued (microVM boot takes ~125ms)", language),
		Success: true,
		Error:   nil,
		ExecutionTime: time.Duration(0),
	}
}
