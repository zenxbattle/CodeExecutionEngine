package logutil

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"time"
)

type entry struct {
	Timestamp  string         `json:"timestamp"`
	Level      string         `json:"level"`
	Service    string         `json:"service"`
	TraceID    string         `json:"traceId"`
	Message    string         `json:"message"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Layer      string         `json:"layer,omitempty"`
	Error      string         `json:"error,omitempty"`
	Caller     string         `json:"caller,omitempty"`
}

type Logger struct {
	service string
}

func New(service string) *Logger {
	return &Logger{service: service}
}

func (l *Logger) Log(level fmt.Stringer, traceID, message string, attributes map[string]any, layer string, err error) {
	if traceID == "" {
		return
	}
	e := entry{
		Timestamp:  time.Now().UTC().Format(time.RFC3339Nano),
		Level:      level.String(),
		Service:    l.service,
		TraceID:    traceID,
		Message:    message,
		Attributes: attributes,
		Layer:      layer,
	}
	if err != nil {
		e.Error = err.Error()
	}
	_, file, line, ok := runtime.Caller(1)
	if ok {
		short := file
		for i := len(file) - 1; i > 0; i-- {
			if file[i] == '/' {
				short = file[i+1:]
				break
			}
		}
		e.Caller = fmt.Sprintf("%s:%d", short, line)
	}
	b, _ := json.Marshal(e)
	fmt.Fprintln(os.Stdout, string(b))
}

func (l *Logger) Close() {}
