package docker

import "time"

type langConfig struct {
	Timeout time.Duration
	Command string
}

var languageConfigs = map[string]langConfig{
	"go": {
		Timeout: 20 * time.Second,
		Command: "export GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod 2>/dev/null; cat > /app/temp/code.go && go build -o /app/temp/exe /app/temp/code.go && /app/temp/exe",
	},
	"js": {
		Timeout: 10 * time.Second,
		Command: "cat > /app/temp/code.js && node /app/temp/code.js",
	},
	"python": {
		Timeout: 10 * time.Second,
		Command: "cat > /app/temp/code.py && python3 /app/temp/code.py",
	},
	"cpp": {
		Timeout: 20 * time.Second,
		Command: "export CCACHE_DIR=/tmp/ccache 2>/dev/null; cat > /app/temp/code.cpp && ccache g++ -o /app/temp/exe /app/temp/code.cpp && /app/temp/exe",
	},
}

func getLanguageConfig(language string) (langConfig, bool) {
	cfg, ok := languageConfigs[language]
	return cfg, ok
}
