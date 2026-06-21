# CodeExecutionEngine

Isolated code execution engine with dual backends: **Docker containers** and **Firecracker microVMs**. Supports Python, JavaScript (Node.js), C++, and Go with 45+ more languages available via guest runner.

## Architecture

```
┌──────────────┐     ┌─────────────────────┐     ┌─────────────────────────┐
│  NATS Queue  │────▶│  CodeExecutionEngine │────▶│  Worker Pool            │
│  (requests)  │     │  (worker.go)         │     │                         │
└──────────────┘     └──────────┬──────────┘     │  ┌───────────────────┐  │
                                │                 │  │ Docker Worker     │  │
                                │                 │  │ docker/docker.go  │  │
                                │                 │  │ container per req │  │
                                │                 │  └───────────────────┘  │
                                │                 │  ┌───────────────────┐  │
                                │                 │  │ Firecracker Worker│  │
                                │                 │  │ fc/firecracker.go │  │
                                │                 │  │ snapshot restore  │  │
                                │                 │  │ + vsock inject    │  │
                                │                 │  └────────┬──────────┘  │
                                │                            │              │
                                │              ┌─────────────▼───────────┐  │
                                │              │  /dev/vhost-vsock  UDS  │  │
                                │              └─────────────┬───────────┘  │
                                │                            │              │
                                │              ┌─────────────▼───────────┐  │
                                │              │  Firecracker microVM    │  │
                                │              │  ┌───────────────────┐  │  │
                                │              │  │ guest-runner      │  │  │
                                │              │  │ (vsock port 5000) │  │  │
                                │              │  │ python3/node/g++  │  │  │
                                │              │  │ go/etc.           │  │  │
                                │              │  └───────────────────┘  │  │
                                │              └─────────────────────────┘  │
                                └───────────────────────────────────────────┘
```

## Worker Interface

Both backends implement the same `worker.WorkerPool` interface:

```go
type WorkerPool interface {
    Initialize() error                           // setup (pull image / create snapshot)
    ExecuteJob(language, code string) (Result, error)  // run code
    Shutdown() error                             // cleanup
    Stats() PoolStats                            // metrics
}
```

Switch backends via `WORKER_TYPE=docker` or `WORKER_TYPE=firecracker` in `.env`.

---

## Docker Worker

**File:** `worker/docker/docker.go`

Per-request flow:
```
docker run --rm -i --network=none --memory=256m --cpus=0.5 \
    --cap-drop=ALL --security-opt=no-new-privileges \
    <image> python3 -c "<code>"
```

- Pre-pulled worker image with runtimes
- Each request spawns and destroys a container
- Process namespace isolation + cgroup limits
- Shared host kernel

**Latency:** ~350ms p50 (Python), ~500-1000ms for compiled languages

---

## Firecracker Worker (Default)

**Files:** `worker/firecracker/firecracker.go`, `worker/firecracker/vsock/vsock.go`

### What is Firecracker?

Firecracker is a lightweight VMM (Virtual Machine Monitor) built by AWS for Lambda and Fargate. It provides:

- **Hardware isolation:** Each execution runs in its own KVM microVM with a dedicated kernel
- **Minimal footprint:** ~5MB overhead per VM, ~125ms cold boot
- **Production track record:** Powers millions of AWS Lambda invocations per second

### How It Works

The Firecracker worker uses a **two-phase** approach:

#### Phase 1: Snapshot Creation (one-time, on Initialize)

```
1. Build rootfs (.ext4 disk image) with runtimes + guest-runner
2. Cold boot a Firecracker VM with this rootfs
3. Guest-runner starts and listens on vsock port 5000
4. Pause the VM (freeze CPU/memory state)
5. Create snapshot: save all RAM (512MB) + device state (14KB) to disk
```

#### Phase 2: Per-Request Execution

For each code execution request:

```
Host (our engine)                     Guest VM (inside Firecracker)
    │                                       │
    ├── firecracker --api-sock (10ms)       │
    ├── PUT /snapshot/load resume_vm (5ms)  │  ← VM resumes INSTANTLY
    │                                       │  ← guest-runner already alive
    ├── net.Dial(vsock UDS)                 │
    ├── Write "CONNECT 5000\n"             │  ← vsock handshake
    │                                    ←─┼── Read request
    ├── Write [4B len][JSON code req]  →   │
    │                                    ←─┼── Spawn python3/node/g++/go
    │                                    ←─┼── Write [4B len][JSON result]
    ├── Read response                       │
    ├── Kill firecracker process (5ms)      │
    │                                       │
    Total: ~40ms (vs Docker's 350ms)
```

### Vsock Communication

Vsock is Firecracker's zero-copy host-guest communication channel:

```
Firecracker exposes a Unix socket on the host (/tmp/fc-*.vsock)
                    │
     ┌──────────────┼──────────────┐
     │  Host sends:                │
     │  "CONNECT 5000\n"           │  ← multiplexing handshake
     │  [4-byte big-endian length] │  ← framing
     │  [JSON request body]        │  ← payload
     │                             │
     │  Guest responds:            │
     │  [4-byte big-endian length] │
     │  [JSON response body]       │
     └─────────────────────────────┘
```

Data flows through a **shared memory ring buffer** — no TCP stack, no network overhead.

### Guest Runner

Based on [andrebassi/runner-codes](https://github.com/andrebassi/runner-codes), the guest runner is a static Go binary (~3.8MB) that runs as PID 1 inside the VM. It:

- Listens on vsock port 5000
- Receives length-prefixed JSON execution requests
- Spawns language runtimes with proper process group isolation
- Returns JSON results with exit codes and timing

**Supported languages:** python, js, node, cpp, go, rust, java, ruby, php, perl, haskell, lua, and 40+ more.

---

## Rootfs

**Location:** `/var/lib/firecracker/worker-rootfs.ext4` (800MB sparse ext4)

### Contents

| Component | Path | Source |
|-----------|------|--------|
| guest-runner | `/bin/guest-runner` | Static Go binary (CGO_ENABLED=0, 3.8MB) |
| python3 | `/usr/bin/python3` | Python 3.12 |
| node | `/usr/bin/node` | Node.js 22 |
| g++ | `/usr/bin/g++` | GCC 13 |
| go | `/usr/lib/go/bin/go` | Go 1.21 |
| busybox | `/bin/busybox` | Shell, utilities |
| init script | `/init` | Mounts proc/sys/dev, starts guest-runner |

### Building the Rootfs

```bash
# 1. Build Docker image with all runtimes
docker build -t zenx-rootfs -f- . << 'EOF'
FROM alpine:3.19
RUN apk add --no-cache python3 nodejs g++ go bash
EOF

# 2. Extract to ext4
CONTAINER=$(docker create zenx-rootfs)
docker export $CONTAINER -o /tmp/rootfs.tar
dd if=/dev/zero of=/var/lib/firecracker/worker-rootfs.ext4 bs=1M count=800
mkfs.ext4 -F /var/lib/firecracker/worker-rootfs.ext4
mount -o loop /var/lib/firecracker/worker-rootfs.ext4 /mnt
tar -xf /tmp/rootfs.tar -C /mnt/

# 3. Copy guest-runner and init
cp guest-runner /mnt/bin/
cat > /mnt/init << 'INIT'
#!/bin/sh
mount -t proc proc /proc; mount -t sysfs sys /sys; mount -t devtmpfs dev /dev
export PATH=/usr/bin:/bin:/sbin:/usr/lib/go/bin GOROOT=/usr/lib/go HOME=/root
exec /bin/guest-runner
INIT
chmod +x /mnt/init
umount /mnt
```

### Creating the Snapshot

```bash
# Boot VM, verify guest-runner is up, pause, snapshot
firecracker --api-sock /tmp/fc.sock --id snap &
curl --unix-socket /tmp/fc.sock -X PUT http://localhost/machine-config \
  -d '{"vcpu_count":1,"mem_size_mib":512}'
curl --unix-socket /tmp/fc.sock -X PUT http://localhost/boot-source \
  -d '{"kernel_image_path":"/var/lib/firecracker/vmlinux","boot_args":"console=ttyS0 reboot=k panic=1 pci=off init=/init root=/dev/vda rw"}'
curl --unix-socket /tmp/fc.sock -X PUT http://localhost/drives/rootfs \
  -d '{"drive_id":"rootfs","path_on_host":"/var/lib/firecracker/worker-rootfs.ext4","is_root_device":true}'
curl --unix-socket /tmp/fc.sock -X PUT http://localhost/vsock \
  -d '{"guest_cid":3,"uds_path":"/tmp/fc-snap.vsock"}'
curl --unix-socket /tmp/fc.sock -X PUT http://localhost/actions \
  -d '{"action_type":"InstanceStart"}'
# Wait for guest-runner (~5s)

# Pause and snapshot
curl --unix-socket /tmp/fc.sock -X PATCH http://localhost/vm \
  -d '{"state":"Paused"}'
curl --unix-socket /tmp/fc.sock -X PUT http://localhost/snapshot/create \
  -d '{"snapshot_type":"Full","snapshot_path":".../vmstate.snapshot","mem_file_path":".../mem.snapshot"}'
```

---

## Configuration

**File:** `config/config.go` — loaded from `.env`

| Variable | Default | Description |
|----------|---------|-------------|
| `WORKER_TYPE` | `firecracker` | `"docker"` or `"firecracker"` |
| `MAX_WORKERS` | auto | Concurrent workers (auto-calculated if `AUTO_SPAWN=true`) |
| `WORKER_IMAGE` | `lijuthomas/zenxbattle-engine-worker:latest` | Docker image for Docker worker |
| `NATSURL` | `nats://localhost:4222` | NATS queue URL |
| `PORT` | `50053` | HTTP health port |

### Switching Backends

```bash
# Use Docker
export WORKER_TYPE=docker
./engine

# Use Firecracker (default, recommended)
export WORKER_TYPE=firecracker
./engine
```

---

## Benchmark: Case Study

### Test Setup

| Parameter | Value |
|-----------|-------|
| Hardware | MSI Modern 14 B5M, AMD Ryzen 7 5700U (8C/16T), 16GB RAM |
| OS | Fedora 40, Linux 6.x, KVM enabled |
| Docker image | `zenx-rootfs:latest` (Alpine 3.19, 562MB, pre-pulled) |
| FC snapshot | `mem.snapshot` (512MB) + `vmstate.snapshot` (14KB) |
| Code | `print(42)` / `console.log(42)` / C++ hello world / Go hello world |
| Requests | 20 per concurrency level |

### Per-Language Latency (p50, serial execution)

```
Language    Docker (p50)    Firecracker (p50)    Speedup
─────────────────────────────────────────────────────────
Python      646ms            39ms                  16.6x
JavaScript   565ms            19ms                  29.7x
C++         884ms           441ms                   2.0x
Go          798ms            28ms                  28.5x
```

### Concurrency Scaling (Python, 20 requests)

```
Concurrency    Docker p50    Docker p95    FC p50    FC p95
────────────────────────────────────────────────────────────
1              322ms         620ms         40ms      57ms
5              565ms         699ms         43ms      57ms
10             1,373ms       2,075ms       43ms      57ms
```

**Key finding:** Firecracker latency is flat (40-43ms p50) regardless of load. Docker degrades 4x from concurrency 1 to 10 (322ms → 1,373ms p50).

### Latency Breakdown (Firecracker, Python)

```
Step                              Time
─────────────────────────────────────────
firecracker process start          ~10ms
snapshot load (mmap RAM)            ~5ms
vsock UDS discovery                 ~5ms
CONNECT handshake                   ~2ms
JSON encode + write                 ~1ms
python3 execution                   ~12ms
JSON decode + read                  ~1ms
firecracker process kill            ~5ms
─────────────────────────────────────────
Total                             ~40ms
```

### Why Firecracker Wins

1. **No container creation overhead:** Docker must create namespaces, setup cgroups, mount overlayfs, start an init process. Firecracker just mmap's a RAM file.

2. **No binary invocation latency:** The guest-runner is already running inside the frozen VM. When restored, it's mid-accept-loop, ready for the next connection.

3. **Vsock vs stdin piping:** Docker uses shell stdin piping (`echo code | python3`). Vsock is a zero-copy shared memory ring buffer — no shell, no pipe, no byte copying.

4. **C++ limitation:** Compilation time (`g++`) dominates both backends equally, making Firecracker's advantage smaller for compiled languages.

### Isolation Comparison

| Property | Docker | Firecracker |
|----------|--------|-------------|
| Kernel | Shared with host | Dedicated per VM |
| Memory isolation | cgroups (soft, bypassable) | Hardware EPT (enforced by CPU) |
| Attack surface | Full host syscall table | Firecracker's minimal device model (~50 ioctls) |
| Escape surface | Linux kernel (any CVE) | KVM + Firecracker's reduced emulated devices |
| Multi-tenant safety | Not recommended | Production-grade (AWS Lambda) |
| Boot time | instant (process) | ~5ms (snapshot restore) |
| Memory per instance | ~10-50MB | 512MB (adjustable) |

### Historical Context

This started as a Docker-only engine. Firecracker support went through multiple iterations:

- **v1 (cold boot):** Full kernel boot per request — 1.3s latency. Too slow.
- **v2 (stdin pipes):** Tried injecting code via serial console — kernel limitation, no stdin after boot.
- **v3 (PTY approach):** Tried wiring PTY to serial — Firecracker can't forward serial input in API mode.
- **v4 (tap networking):** Tried HTTP via tap device — complex network setup, slow.
- **v5 (snapshot + vsock):** Adopted [runner-codes](https://github.com/andrebassi/runner-codes) approach — 40ms per request, 16-29x faster than Docker.

---

## Project Structure

```
CodeExecutionEngine/
├── cmd/main.go              # Entry point: loads config, creates pool, connects NATS
├── config/config.go         # ENV-based configuration
├── executor/                # Container management, rate limiting
├── internal/sanitize.go     # Code sanitization
├── logger/                  # Structured logging (zap + BetterStack)
├── model/                   # Data models
├── natsclient/              # NATS messaging client
├── service/                 # Business logic layer
├── worker/
│   ├── worker.go            # WorkerPool interface
│   ├── docker/
│   │   ├── docker.go        # Docker worker: container-per-request
│   │   └── config.go        # Language configs (timeout, command templates)
│   └── firecracker/
│       ├── firecracker.go   # Firecracker worker: snapshot restore + vsock
│       └── vsock/
│           └── vsock.go     # Vsock client: CONNECT handshake + JSON framing
├── Dockerfile               # Engine build
├── Dockerfile.worker        # Worker image (all runtimes)
└── deploy/                  # K3s deployment manifests
```

---

## Dependencies

- **Docker:** For Docker worker + rootfs building
- **Firecracker v1.16.0:** `/usr/local/bin/firecracker`
- **KVM:** `/dev/kvm` (must exist)
- **vhost_vsock:** `/dev/vhost-vsock` (kernel module `vhost_vsock`)
- **NATS:** Message queue for distributed request dispatch

---

## Quick Start

```bash
# 1. Verify prerequisites
ls /dev/kvm /dev/vhost-vsock  # both must exist
firecracker --version         # v1.16.0+

# 2. Set environment
export WORKER_TYPE=firecracker  # or "docker"
export NATSURL=nats://localhost:4222

# 3. Build and run
go build -o engine ./cmd
./engine
```

---

## Docker Optimizations

The baseline Docker worker spawns a fresh container per request (`docker run --rm`). Three progressively faster approaches exist:

### Comparison of Approaches

| Approach | Latency (Python p50) | vs Baseline | How |
|----------|---------------------|-------------|-----|
| `docker run --rm` (baseline) | **350ms** | 1x | Full container lifecycle: namespaces → cgroups → overlayfs → init → exec → destroy |
| `docker exec` into warm container | **87ms** | 4x faster | Container pre-started, only exec overhead |
| **Pre-warmed Unix socket agent** | **31ms** | 11x faster | Long-lived Go agent inside container, communicates via Unix socket bind-mounted from host |
| **Firecracker snapshot** | **40ms** | 8.7x faster | mmap'd RAM restore + vsock injection |

### Pre-Warmed Docker Agent (dockerd)

The fastest Docker approach uses a small Go agent (~3.9MB, CGO_ENABLED=0) running inside one or more long-lived containers:

```go
// Inside container: listens on Unix socket, receives JSON, spawns runtimes
l, _ := net.Listen("unix", "/tmp/dockerd.sock")
for {
    conn, _ := l.Accept()
    // read {"Lang":"python","Code":"print(42)"}
    // exec python3, capture output
    // write {"Stdout":"42\n","ExitCode":0,"Ms":40}
}
```

```
Host                          Container (pre-warmed, alive forever)
  │                                  │
  │  echo '{"Lang":"python",        │
  │       "Code":"print(42)"}' |     │
  │  nc -U /tmp/dockerd.sock  ──→   │  dockerd receives
  │                              ←───│  dockerd spawns python3
  │  {"Stdout":"42\n",...}    ←───  │  dockerd writes result
  │                                  │
  Total: 31ms (includes I/O)
```

```bash
# Build and run the pre-warmed agent
docker build -t zenx-dockerd .
docker run -d --name dockerd-agent \
  -v /tmp/dockerd-sock:/tmp \
  zenx-dockerd

# Send code
echo '{"Lang":"python","Code":"print(42)"}' \
  | nc -U /tmp/dockerd-sock/dockerd.sock

# → {"Stdout":"42\n","ExitCode":0,"Ms":46}
```

### Trade-Off Matrix

| Factor | docker run | docker exec | docker socket agent | Firecracker snapshot |
|--------|-----------|-------------|-------------------|---------------------|
| **Latency (p50)** | 350ms | 87ms | 31ms | 40ms |
| **Isolation** | Process ns + cgroups | Process ns + cgroups | Process ns + cgroups | **Hardware KVM VM** |
| **Kernel** | Shared | Shared | Shared | **Dedicated** |
| **Memory per instance** | ~10-50MB | ~30MB (shared container) | ~30MB (shared container) | 512MB |
| **Concurrency** | Unlimited | Limited by container count | Limited by agent goroutines | Limited by RAM per VM |
| **Multi-tenant safe** | No (shared kernel CVE surface) | No | No | **Yes (Lambda-grade)** |
| **Setup complexity** | None | None | Build tiny Go binary | Build rootfs + snapshot |
| **Best for** | Dev, low volume | Moderate throughput | **High throughput + Docker** | **High throughput + Security** |

### When to Use Which

- **Docker run:** Quick dev testing, low request volume. Simplest setup.
- **Docker exec:** Moderate load where Docker ecosystem is required. 4x faster than baseline.
- **Docker socket agent:** Maximum Docker throughput. 11x faster than baseline. Still shares host kernel.
- **Firecracker snapshot:** Untrusted code, multi-tenant platforms, production sandboxing. Hardware isolation at 40ms.

### Implementation Status

The engine currently implements:

| Implementation | Status | File |
|---------------|--------|------|
| docker run (baseline) | Production | `worker/docker/docker.go` |
| firecracker snapshot | Production | `worker/firecracker/firecracker.go` |
| docker socket agent | Proof of concept | `/tmp/dockerd/` (migrate to `worker/docker/socket.go` for production) |

The socket agent can be promoted to `worker/docker/socket.go` as a drop-in `WorkerPool` that maintains N pre-warmed containers, each running dockerd, with round-robin dispatch. This would bring Docker latency from 350ms → 31ms without changing the security model.

## References

- [Firecracker microVM](https://github.com/firecracker-microvm/firecracker) — AWS Labs
- [runner-codes](https://github.com/andrebassi/runner-codes) — multi-language code execution via Firecracker + vsock
- [Firecracker snapshot/restore](https://github.com/firecracker-microvm/firecracker/blob/main/docs/snapshotting/snapshot-support.md)
- [Vsock protocol](https://man7.org/linux/man-pages/man7/vsock.7.html)
- [AWS Firecracker design](https://firecracker-microvm.github.io/)
