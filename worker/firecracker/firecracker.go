package firecracker

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"zenxbattle/worker"
)

type vmInstance struct {
	cmd    *exec.Cmd
	udsVS  string
}

type FCPool struct {
	cfg      worker.Config
	stats    worker.PoolStats
	mu       sync.Mutex
	jobs     chan worker.Job
	shutdown chan struct{}
	wg       sync.WaitGroup
	vms      []*vmInstance
}

func NewPool(cfg worker.Config) (*FCPool, error) {
	if cfg.MaxWorkers <= 0 { cfg.MaxWorkers = 2 }
	return &FCPool{jobs: make(chan worker.Job, cfg.MaxWorkers*10), shutdown: make(chan struct{}), cfg: cfg}, nil
}

func (p *FCPool) Initialize() error {
	for i := 0; i < p.cfg.MaxWorkers; i++ {
		vm, err := p.bootVM(i)
		if err != nil { return fmt.Errorf("worker %d: %w", i, err) }
		p.vms = append(p.vms, vm)
		p.wg.Add(1); go p.worker(i, vm)
	}
	p.stats.ActiveWorkers = len(p.vms)
	return nil
}

func (p *FCPool) bootVM(id int) (*vmInstance, error) {
	api := fmt.Sprintf("/tmp/fc-api-%d.sock", id)
	vs := fmt.Sprintf("/tmp/fc-vsock-%d.sock", id)
	os.Remove(api); os.Remove(vs)

	cmd := exec.Command("firecracker", "--api-sock", api)
	cmd.Start()
	for i := 0; i < 100; i++ { if _, e := os.Stat(api); e == nil { break }; time.Sleep(50 * time.Millisecond) }

	c := &http.Client{Transport: &http.Transport{DialContext: func(_ context.Context, _, _ string) (net.Conn, error) { return net.Dial("unix", api) }}}
	put := func(path, body string) {
		req, _ := http.NewRequest("PUT", "http://localhost"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r, _ := c.Do(req); if r != nil { r.Body.Close() }
	}
	put("/boot-source", `{"kernel_image_path":"/var/lib/firecracker/vmlinux","boot_args":"console=ttyS0 reboot=k panic=1 pci=off init=/init"}`)
	put("/drives/rootfs", `{"drive_id":"rootfs","path_on_host":"/var/lib/firecracker/worker-rootfs.ext4","is_root_device":true,"is_read_only":false}`)
	put("/vsock", fmt.Sprintf(`{"guest_cid":3,"uds_path":"%s"}`, vs))
	put("/machine-config", `{"vcpu_count":1,"mem_size_mib":256,"smt":false}`)
	put("/actions", `{"action_type":"InstanceStart"}`)

	for i := 0; i < 200; i++ { if _, e := os.Stat(vs); e == nil { break }; time.Sleep(50 * time.Millisecond) }
	return &vmInstance{cmd: cmd, udsVS: vs}, nil
}

func (p *FCPool) ExecuteJob(language, code string) (worker.Result, error) {
	res := make(chan worker.Result, 1)
	select {
	case p.jobs <- worker.Job{Language: language, Code: code, Result: res}:
		atomic.AddInt64(&p.stats.TotalExecuted, 1)
		r := <-res; return r, r.Error
	default: return worker.Result{}, fmt.Errorf("fc queue full")
	}
}

func (p *FCPool) Shutdown() error {
	close(p.shutdown); p.wg.Wait()
	for _, v := range p.vms { v.cmd.Process.Kill() }; return nil
}
func (p *FCPool) Stats() worker.PoolStats { return p.stats }

func (p *FCPool) worker(id int, vm *vmInstance) {
	defer p.wg.Done()
	for {
		select {
		case job := <-p.jobs: job.Result <- p.exec(vm, job.Language, job.Code)
		case <-p.shutdown: return
		}
	}
}

func (p *FCPool) exec(vm *vmInstance, lang, code string) worker.Result {
	start := time.Now()
	codeB64 := base64.StdEncoding.EncodeToString([]byte(code))

	conn, err := net.Dial("unix", vm.udsVS)
	if err != nil { return worker.Result{Error: err, ExecutionTime: time.Since(start)} }
	defer conn.Close()

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// Firecracker vsock UDS: first 8 bytes = CID, next 4 = port
	header := make([]byte, 12)
	header[0] = 3 // guest CID
	header[8] = 0x5b; header[9] = 0x15 // port 5555 in LE
	conn.Write(header)
	if _, err := io.WriteString(conn, codeB64); err != nil {
		return worker.Result{Error: fmt.Errorf("write: %w", err), ExecutionTime: time.Since(start)}
	}

	// Read result
	var buf [65536]byte
	n, err := conn.Read(buf[:])
	if err != nil && err != io.EOF {
		return worker.Result{Error: fmt.Errorf("read: %w", err), ExecutionTime: time.Since(start)}
	}

	return worker.Result{
		Output:        strings.TrimSpace(string(buf[:n])),
		Success:       n > 0,
		ExecutionTime: time.Since(start),
	}
}
