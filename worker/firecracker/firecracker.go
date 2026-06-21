package firecracker

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
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
	return &FCPool{
		cfg:      cfg,
		jobs:     make(chan worker.Job, cfg.MaxWorkers*10),
		shutdown: make(chan struct{}),
	}, nil
}

func (p *FCPool) Initialize() error {
	if _, err := os.Stat("/var/lib/firecracker/vmlinux"); err != nil {
		return fmt.Errorf("firecracker kernel not found at /var/lib/firecracker/vmlinux")
	}
	if _, err := os.Stat("/var/lib/firecracker/worker-rootfs.ext4"); err != nil {
		return fmt.Errorf("firecracker rootfs not found at /var/lib/firecracker/worker-rootfs.ext4")
	}
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
		return worker.Result{}, fmt.Errorf("fc queue full")
	}
}

func (p *FCPool) Shutdown() error {
	close(p.shutdown)
	p.wg.Wait()
	return nil
}

func (p *FCPool) Stats() worker.PoolStats {
	p.stats.ActiveWorkers = p.cfg.MaxWorkers
	p.stats.QueuedJobs = len(p.jobs)
	return p.stats
}

func (p *FCPool) worker(id int) {
	defer p.wg.Done()
	for {
		select {
		case job := <-p.jobs:
			result := p.execute(job.Language, job.Code)
			job.Result <- result
		case <-p.shutdown: return
		}
	}
}

func (p *FCPool) execute(language, code string) worker.Result {
	codeB64 := base64.StdEncoding.EncodeToString([]byte(code))
	
	config := fmt.Sprintf(
		`{"boot-source":{"kernel_image_path":"/var/lib/firecracker/vmlinux","boot_args":"console=ttyS0 reboot=k panic=1 pci=off init=/init CODE=%s"},"drives":[{"drive_id":"rootfs","path_on_host":"/var/lib/firecracker/worker-rootfs.ext4","is_root_device":true,"is_read_only":false}],"machine-config":{"vcpu_count":1,"mem_size_mib":256,"smt":false}}`,
		codeB64,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "firecracker", "--no-api", "--config-file", "/dev/stdin")
	cmd.Stdin = strings.NewReader(config)
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	// Extract Python output from kernel noise
	output := extractOutput(stderr.String())

	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return worker.Result{Output: output, Success: false, Error: fmt.Errorf("timeout"), ExecutionTime: duration}
	}

	return worker.Result{
		Output:        output,
		Success:       err == nil && output != "",
		Error:         err,
		ExecutionTime: duration,
	}
}

// extractOutput pulls clean code output from firecracker serial console noise
func extractOutput(stderr string) string {
	lines := strings.Split(stderr, "\n")
	var clean []string
	skipPrefixes := []string{"[", "2026", "Linux", "mount", "Free", "Write", "Kernel", "Command", "BIOS", "x86", "NX", "Hyper", "tsc", "e820", "DMI", "NUMA", "kvm", "Zone", "Memory", "PID", "SLUB", "Hierarch", "RCU", "NR_IRQS", "Console", "Calibrat", "pid_max", "Security", "SELinux", "Dentry", "Inode", "Mount", "Last", "Spectre", "Specul", "smpboot", "x2apic", "Switched", "TIMER", "Performance", "smp:", "devtmpfs", "clocksource", "futex", "NET:", "cpuidle", "HugeTLB", "dmi:", "NetLabel", "VFS", "TCP", "UDP", "UDP-Lite", "virtio", "platform", "Scanning", "audit", "Initialise", "Key", "workingset", "squashfs", "Asymmetric", "Block", "io ", "Serial", "loop:", "tun:", "hidraw", "nf_conntrack", "ip_tables", "Initializ", "Segment", "Bridge", "sched_clock", "registered", "Loading", "Loaded", "zswap", "EXT4", "openrc", "modprobe", "Firecracker"}

LINE:
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" { continue }
		for _, p := range skipPrefixes {
			if strings.HasPrefix(line, p) { continue LINE }
		}
		clean = append(clean, line)
	}
	return strings.TrimSpace(strings.Join(clean, "\n"))
}
