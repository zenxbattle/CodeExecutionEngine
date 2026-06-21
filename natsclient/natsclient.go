package natsclient

import (
	"time"

	"github.com/nats-io/nats.go"
)

func Connect(url string) (*nats.Conn, error) {
	return nats.Connect(url)
}

func Request(nc *nats.Conn, subject string, data []byte, timeout time.Duration) ([]byte, error) {
	msg, err := nc.Request(subject, data, timeout)
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}
