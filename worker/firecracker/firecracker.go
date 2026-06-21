package firecracker

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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
	if cfg.MaxWorkers <= 0 { cfg.MaxWorkers = 2 }
	return &FCPool{jobs: make(chan worker.Job, cfg.MaxWorkers*4), shutdown: make(chan struct{}), cfg: cfg}, nil
}

func (p *FCPool) Initialize() error {
	if _, err := os.Stat("/var/lib/firecracker/vmlinux"); err != nil {
		return fmt.Errorf("firecracker kernel not found")
	}
	if _, err := os.Stat("/var/lib/firecracker/worker-rootfs.ext4"); err != nil {
		return fmt.Errorf("firecracker rootfs not found")
	}
	for i := 0; i < p.cfg.MaxWorkers; i++ { p.wg.Add(1); go p.worker(i) }
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
		return worker.Result{}, fmt.Errorf("fc queue full")
	}
}

func (p *FCPool) Shutdown() error { close(p.shutdown); p.wg.Wait(); return nil }
func (p *FCPool) Stats() worker.PoolStats { return p.stats }

func (p *FCPool) worker(id int) {
	defer p.wg.Done()

	// Boot one microVM per worker - communicate via stdin/stdout
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tmpFile := fmt.Sprintf("/tmp/fc-config-%d.json", id)
	os.WriteFile(tmpFile, []byte(fmt.Sprintf(
		`{"boot-source":{"kernel_image_path":"/var/lib/firecracker/vmlinux","boot_args":"console=ttyS0 reboot=k panic=1 pci=off init=/init"},"drives":[{"drive_id":"rootfs","path_on_host":"/var/lib/firecracker/worker-rootfs.ext4","is_root_device":true,"is_read_only":false}],"machine-config":{"vcpu_count":1,"mem_size_mib":256,"smt":false}}`,
	)), 0644)
	defer os.Remove(tmpFile)

	cmd := exec.CommandContext(ctx, "firecracker", "--no-api", "--config-file", tmpFile)
	stdout, _ := cmd.StdoutPipe()
	stdin, _ := cmd.StdinPipe()
	cmd.Stderr = nil
	cmd.Start()

	// Wait for VM to boot
	buf := bufio.NewScanner(stdout)
	for buf.Scan() {
		if strings.Contains(buf.Text(), "Freeing unused kernel") {
			break
		}
	}

	p.stats.ActiveWorkers++
	defer func() { p.stats.ActiveWorkers-- }()

	// Process jobs
	for {
		select {
		case job := <-p.jobs:
			codeB64 := base64.StdEncoding.EncodeToString([]byte(job.Code))
			start := time.Now()
			io.WriteString(stdin, codeB64+"\n")

			var result string
			for buf.Scan() {
				line := buf.Text()
				if strings.HasPrefix(line, "FC_RESULT:") {
					result = strings.TrimPrefix(line, "FC_RESULT:")
					break
				}
			}

			job.Result <- worker.Result{
				Output:        strings.TrimSpace(result),
				Success:       true,
				ExecutionTime: time.Since(start),
			}
		case <-p.shutdown:
			cancel()
			return
		}
	}
}
