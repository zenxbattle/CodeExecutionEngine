package firecracker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"zenxbattle/worker"
)

// FirecrackerPool implements worker.WorkerPool using Firecracker snapshots
type FirecrackerPool struct {
	cfg          worker.Config
	snapshotDir  string // path to mem.snapshot + vmstate.snapshot
	kernelPath   string
	rootfsPath   string
	stats        worker.PoolStats
	mu           sync.Mutex
	warmSem      chan struct{} // concurrency limiter
	jobs         chan worker.Job
	shutdown     chan struct{}
	wg           sync.WaitGroup
}

func NewPool(cfg worker.Config) (*FirecrackerPool, error) {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 3
	}
	snapshotDir := "/var/lib/firecracker/snapshots/codeexec"
	if cfg.Image != "" {
		snapshotDir = filepath.Join("/var/lib/firecracker/snapshots", cfg.Image)
	}
	return &FirecrackerPool{
		cfg:         cfg,
		snapshotDir: snapshotDir,
		kernelPath:  "/var/lib/firecracker/vmlinux",
		rootfsPath:  "/var/lib/firecracker/worker-rootfs.ext4",
		warmSem:     make(chan struct{}, cfg.MaxWorkers),
		jobs:        make(chan worker.Job, cfg.MaxWorkers*10),
		shutdown:    make(chan struct{}),
	}, nil
}

// Initialize cold-boots a VM and creates a snapshot for fast restores
func (p *FirecrackerPool) Initialize() error {
	// Check if snapshot already exists
	memPath := filepath.Join(p.snapshotDir, "mem.snapshot")
	vmstatePath := filepath.Join(p.snapshotDir, "vmstate.snapshot")
	if _, err := os.Stat(memPath); err == nil {
		if _, err := os.Stat(vmstatePath); err == nil {
			// Snapshot already exists
			p.startWorkers()
			return nil
		}
	}

	// Create snapshot
	os.MkdirAll(p.snapshotDir, 0755)
	if err := p.createSnapshot(memPath, vmstatePath); err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}

	p.startWorkers()
	return nil
}

func (p *FirecrackerPool) startWorkers() {
	for i := 0; i < p.cfg.MaxWorkers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
	p.stats.ActiveWorkers = p.cfg.MaxWorkers
}

// createSnapshot boots a VM and takes a full snapshot
func (p *FirecrackerPool) createSnapshot(memPath, vmstatePath string) error {
	sock := "/tmp/fc-init.sock"
	vsockPath := "/tmp/fc-init.vsock"
	os.Remove(sock)
	os.Remove(vsockPath)

	cmd := exec.Command("firecracker", "--api-sock", sock, "--id", "init")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start firecracker: %w", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		os.Remove(sock)
	}()

	// Wait for API socket
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	client := newFCClient(sock)

	// Configure
	if err := client.put("/machine-config", map[string]any{"vcpu_count": 1, "mem_size_mib": 256, "smt": false}); err != nil {
		return fmt.Errorf("machine config: %w", err)
	}
	if err := client.put("/boot-source", map[string]any{
		"kernel_image_path": p.kernelPath,
		"boot_args":         "console=ttyS0 reboot=k panic=1 pci=off init=/init root=/dev/vda rw",
	}); err != nil {
		return fmt.Errorf("boot source: %w", err)
	}
	if err := client.put("/drives/rootfs", map[string]any{
		"drive_id":       "rootfs",
		"path_on_host":   p.rootfsPath,
		"is_root_device": true,
		"is_read_only":   false,
	}); err != nil {
		return fmt.Errorf("drive: %w", err)
	}
	if err := client.put("/vsock", map[string]any{"guest_cid": 3, "uds_path": vsockPath}); err != nil {
		return fmt.Errorf("vsock: %w", err)
	}
	if err := client.put("/actions", map[string]string{"action_type": "InstanceStart"}); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	// Wait for guest-runner to be ready via vsock
	time.Sleep(3 * time.Second)
	vc := NewVsockClient(vsockPath, DefaultVsockPort)
	for i := 0; i < 30; i++ {
		if err := vc.Ping(); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Pause VM
	if err := client.patch("/vm", map[string]string{"state": "Paused"}); err != nil {
		return fmt.Errorf("pause: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Create snapshot
	if err := client.put("/snapshot/create", map[string]any{
		"snapshot_type": "Full",
		"snapshot_path": vmstatePath,
		"mem_file_path": memPath,
	}); err != nil {
		return fmt.Errorf("snapshot create: %w", err)
	}

	fmt.Printf("Snapshot created: %s (%s)\n", vmstatePath, memPath)
	return nil
}

// worker processes jobs from the queue
func (p *FirecrackerPool) worker(id int) {
	defer p.wg.Done()
	for {
		select {
		case job := <-p.jobs:
			// Acquire warm semaphore
			p.warmSem <- struct{}{}
			result := p.executeCode(job.Language, job.Code)
			<-p.warmSem
			job.Result <- result
		case <-p.shutdown:
			return
		}
	}
}

// executeCode restores a VM from snapshot and executes code via vsock
func (p *FirecrackerPool) executeCode(language, code string) worker.Result {
	start := time.Now()

	instanceID := fmt.Sprintf("exec-%d", time.Now().UnixNano())
	sock := fmt.Sprintf("/tmp/fc-%s.sock", instanceID)
	os.Remove(sock)

	cmd := exec.Command("firecracker", "--api-sock", sock, "--id", instanceID)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return worker.Result{Error: fmt.Errorf("start fc: %w", err), ExecutionTime: time.Since(start)}
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
		os.Remove(sock)
	}()

	// Wait for API socket
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	client := newFCClient(sock)

	// Restore snapshot with resume_vm=true
	restoreStart := time.Now()
	memPath := filepath.Join(p.snapshotDir, "mem.snapshot")
	vmstatePath := filepath.Join(p.snapshotDir, "vmstate.snapshot")

	if err := client.put("/snapshot/load", map[string]any{
		"snapshot_path": vmstatePath,
		"mem_backend":   map[string]string{"backend_type": "File", "backend_path": memPath},
		"enable_diff_snapshots": false,
		"resume_vm":     true,
	}); err != nil {
		return worker.Result{Error: fmt.Errorf("load snapshot: %w", err), ExecutionTime: time.Since(start)}
	}
	restoreTime := time.Since(restoreStart)

	// Find vsock UDS - it's created at the path from the snapshot config
	// The snapshot VM was configured with vsock at fc-init.vsock
	// After restore, Firecracker creates it at a new path based on the instance ID
	// Try common patterns
	vsockPath := p.findVsockUDS(instanceID)
	if vsockPath == "" {
		return worker.Result{Error: fmt.Errorf("vsock UDS not found"), ExecutionTime: time.Since(start)}
	}
	defer os.Remove(vsockPath)

	// Wait briefly for vsock to be ready
	time.Sleep(50 * time.Millisecond)

	// Execute via vsock
	vc := NewVsockClient(vsockPath, DefaultVsockPort)
	resp, _, err := vc.Execute(language, code, 10)
	if err != nil {
		return worker.Result{Error: fmt.Errorf("execute: %w", err), ExecutionTime: time.Since(start)}
	}

	total := time.Since(start)
	output := resp.Stdout
	if resp.Stderr != "" {
		output += resp.Stderr
	}

	success := resp.ExitCode == 0 && resp.Error == ""
	var execErr error
	if !success {
		execErr = fmt.Errorf("exit=%d: %s", resp.ExitCode, resp.Error)
	}

	return worker.Result{
		Output:        output,
		Success:       success,
		Error:         execErr,
		ExecutionTime: total,
	}
}

// findVsockUDS scans for the vsock UDS created by the restored snapshot
func (p *FirecrackerPool) findVsockUDS(instanceID string) string {
	candidates := []string{
		fmt.Sprintf("/tmp/fc-%s.vsock", instanceID),
		"/tmp/fc-init.vsock", // original from snapshot config
	}

	// Also scan /tmp for any .vsock file
	entries, err := os.ReadDir("/tmp")
	if err == nil {
		for _, e := range entries {
			n := e.Name()
			if len(n) > 6 && n[len(n)-6:] == ".vsock" {
				candidates = append(candidates, filepath.Join("/tmp", n))
			}
		}
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

func (p *FirecrackerPool) ExecuteJob(language, code string) (worker.Result, error) {
	res := make(chan worker.Result, 1)
	select {
	case p.jobs <- worker.Job{Language: language, Code: code, Result: res}:
		atomic.AddInt64(&p.stats.TotalExecuted, 1)
		r := <-res
		return r, r.Error
	default:
		return worker.Result{}, fmt.Errorf("firecracker queue full")
	}
}

func (p *FirecrackerPool) Shutdown() error {
	close(p.shutdown)
	p.wg.Wait()
	close(p.warmSem)
	return nil
}

func (p *FirecrackerPool) Stats() worker.PoolStats {
	p.stats.ActiveWorkers = p.cfg.MaxWorkers
	p.stats.QueuedJobs = len(p.jobs)
	return p.stats
}

// fcClient is a minimal Firecracker API client over Unix socket
type fcClient struct {
	*http.Client
}

func newFCClient(sock string) *fcClient {
	return &fcClient{&http.Client{
		Transport: &http.Transport{
			DialContext: func(_ interface{}, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
		Timeout: 10 * time.Second,
	}}
}

func (c *fcClient) put(path string, v any) error {
	return c.doReq("PUT", path, v)
}

func (c *fcClient) patch(path string, v any) error {
	return c.doReq("PATCH", path, v)
}

func (c *fcClient) doReq(method, path string, v any) error {
	b, _ := json.Marshal(v)
	req, _ := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%d: %s", resp.StatusCode, string(body))
	}
	return nil
}
