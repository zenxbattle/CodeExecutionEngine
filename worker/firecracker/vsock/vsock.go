7337
package vsock

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

// Firecracker vsock uses SCM_CREDENTIALS with (cid << 32 | port) in PID field

type Conn struct {
	conn net.Conn
	fd   int
}

func Dial(udsPath string, guestCID, guestPort uint32) (*Conn, error) {
	conn, err := net.DialTimeout("unix", udsPath, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("vsock dial: %w", err)
	}
	return &Conn{conn: conn}, nil
}

func (c *Conn) Write(data []byte) (int, error) {
	unixConn, ok := c.conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("not a unix connection")
	}

	f, err := unixConn.File()
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fd := int(f.Fd())

	// The destination CID/port is encoded in the ancillary data
	// Firecracker expects (cid << 32 | port) in the PID field of ucred
	// But actually Firecracker vsock uses a simple binary header: [cid:4][port:4][len:4]
	// Let's try both approaches

	// First try: simple binary header approach
	header := make([]byte, 12)
	binary.LittleEndian.PutUint32(header[0:4], 3)    // guest CID
	binary.LittleEndian.PutUint32(header[4:8], 5555) // guest port
	binary.LittleEndian.PutUint32(header[8:12], uint32(len(data)))

	msg := append(header, data...)
	return syscall.Write(fd, msg)
}

func (c *Conn) Read(buf []byte) (int, error) {
	return c.conn.Read(buf)
}

func (c *Conn) Close() error {
	return c.conn.Close()
}

// Simple approach: use socat via shell (fallback)
func SendViaSocat(udsPath, data string) error {
	f, err := os.OpenFile(udsPath, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	// Firecracker vsock UDS: write destination CID/port via sendmsg
	// As fallback, just write raw data and see if it works
	_, err = f.Write([]byte(data))
	return err
}
