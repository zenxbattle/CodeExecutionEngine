package service

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"zenxbattle/internal"
	"zenxbattle/worker"
)

type CompilerService struct {
	pool       worker.WorkerPool
	ShowOutput bool
}

func NewCompilerService(pool worker.WorkerPool, showOutput bool) *CompilerService {
	return &CompilerService{pool: pool, ShowOutput: showOutput}
}

type CompilerResponse struct {
	Output        string `json:"output"`
	Error         string `json:"error,omitempty"`
	StatusMessage string `json:"status_message"`
	Success       bool   `json:"success"`
	ExecutionTime string `json:"execution_time,omitempty"`
}

func (s *CompilerService) Execute(code, language string) *CompilerResponse {
	start := time.Now()
	language = normalizeLanguage(language)

	codeBytes, err := base64.StdEncoding.DecodeString(code)
	if err != nil {
		return &CompilerResponse{Success: false, Error: err.Error(), StatusMessage: "Failed to decode base64"}
	}

	decoded := string(codeBytes)
	if err := internal.SanitizeCode(decoded, language, 10000); err != nil {
		return &CompilerResponse{Success: false, Error: err.Error(), StatusMessage: err.Error()}
	}

	result, err := s.pool.ExecuteJob(language, decoded)
	if err != nil {
		return &CompilerResponse{Success: false, Error: err.Error(), StatusMessage: "Failed to execute code"}
	}
	if result.Error != nil {
		return &CompilerResponse{Success: false, Error: result.Error.Error(), Output: result.Output, StatusMessage: "Failed to execute code"}
	}

	return &CompilerResponse{
		Success:       true,
		Output:        result.Output,
		ExecutionTime: fmt.Sprintf("%v", time.Since(start)),
		StatusMessage: "Success",
	}
}

func normalizeLanguage(lang string) string {
	lang = strings.ToLower(lang)
	switch {
	case strings.Contains(lang, "py"): return "python"
	case strings.Contains(lang, "js"): return "js"
	case strings.Contains(lang, "go"): return "go"
	case strings.Contains(lang, "c"): return "cpp"
	default: return lang
	}
}
