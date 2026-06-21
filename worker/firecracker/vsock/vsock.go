package firecracker

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	DefaultVsockPort   = 5000
	DefaultGuestCID    = 3
	VsockHandshakeTimeout = 5 * time.Second
	VsockRequestTimeout    = 30 * time.Second
)

type VsockClient struct {
	udsPath string
	port    uint32
}

func NewVsockClient(udsPath string, port uint32) *VsockClient {
	return &VsockClient{udsPath: udsPath, port: port}
}

// connect creates a vsock connection through Firecracker's UDS
// using the CONNECT <port>\n handshake protocol
func (vc *VsockClient) connect() (net.Conn, error) {
	conn, err := net.DialTimeout("unix", vc.udsPath, VsockHandshakeTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial UDS %s: %w", vc.udsPath, err)
	}

	// Firecracker vsock handshake
	handshake := fmt.Sprintf("CONNECT %d\n", vc.port)
	if _, err := conn.Write([]byte(handshake)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send CONNECT: %w", err)
	}

	// Read response: "OK <payload>\n"
	resp := make([]byte, 128)
	n, err := conn.Read(resp)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT resp: %w", err)
	}

	if n < 2 || string(resp[:2]) != "OK" {
		conn.Close()
		return nil, fmt.Errorf("vsock handshake failed: %q", string(resp[:n]))
	}

	return conn, nil
}

// req is the guest-runner request format
type executeReq struct {
	TraceID string `json:"trace_id"`
	Lang    string `json:"lang"`
	Code    string `json:"code"`
	Timeout int    `json:"timeout"`
}

// resp is the guest-runner response format
type executeResp struct {
	Stdout        string `json:"stdout"`
	Stderr        string `json:"stderr"`
	ExitCode      int    `json:"exit_code"`
	ExecutionTime int64  `json:"execution_time"`
	Error         string `json:"error,omitempty"`
	TimedOut      bool   `json:"timed_out,omitempty"`
}

// Execute sends a code execution request via vsock and returns the result.
// Returns wall-clock duration and the parsed response.
func (vc *VsockClient) Execute(lang, code string, timeoutSec int) (*executeResp, time.Duration, error) {
	conn, err := vc.connect()
	if err != nil {
		return nil, 0, fmt.Errorf("vsock connect: %w", err)
	}
	defer conn.Close()

	req := executeReq{
		TraceID: fmt.Sprintf("tr-%d", time.Now().UnixNano()),
		Lang:    lang,
		Code:    code,
		Timeout: timeoutSec,
	}

	data, err := json.Marshal(&req)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}

	// Write length-prefixed JSON (4 bytes big-endian)
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(data)))

	deadline := time.Now().Add(VsockRequestTimeout)
	conn.SetDeadline(deadline)

	if _, err := conn.Write(lenBuf); err != nil {
		return nil, 0, fmt.Errorf("write len: %w", err)
	}
	if _, err := conn.Write(data); err != nil {
		return nil, 0, fmt.Errorf("write req: %w", err)
	}

	// Read response length
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return nil, 0, fmt.Errorf("read resp len: %w", err)
	}
	respLen := binary.BigEndian.Uint32(lenBuf)
	if respLen > 1024*1024 { // 1MB sanity cap
		return nil, 0, fmt.Errorf("response too large: %d", respLen)
	}

	// Read response body
	respData := make([]byte, respLen)
	if _, err := io.ReadFull(conn, respData); err != nil {
		return nil, 0, fmt.Errorf("read resp: %w", err)
	}

	var result executeResp
	if err := json.Unmarshal(respData, &result); err != nil {
		return nil, 0, fmt.Errorf("unmarshal resp: %w", err)
	}

	return &result, time.Until(deadline), nil
}

// Ping checks if the guest runner is alive
func (vc *VsockClient) Ping() error {
	conn, err := vc.connect()
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}
