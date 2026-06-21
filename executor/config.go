7337
package executor

import "time"

type LanguageConfig struct {
	Timeout  time.Duration
	Commands []string
}

var languageConfigs = map[string]LanguageConfig{
	"go": {
		Timeout: 20 * time.Second,
		Commands: []string{
			"sh", "-c",
			"export GOCACHE=/tmp/go-cache GOMODCACHE=/tmp/go-mod 2>/dev/null; cat > /app/temp/code.go && go build -o /app/temp/exe /app/temp/code.go && /app/temp/exe",
		},
	},
	"js": {
		Timeout: 10 * time.Second,
		Commands: []string{
			"sh", "-c",
			"cat > /app/temp/code.js && node /app/temp/code.js",
		},
	},
	"python": {
		Timeout: 10 * time.Second,
		Commands: []string{
			"sh", "-c",
			"cat > /app/temp/code.py && python3 /app/temp/code.py",
		},
	},
	"cpp": {
		Timeout: 20 * time.Second,
		Commands: []string{
			"sh", "-c",
			"export CCACHE_DIR=/tmp/ccache 2>/dev/null; cat > /app/temp/code.cpp && ccache g++ -o /app/temp/exe /app/temp/code.cpp && /app/temp/exe",
		},
	},
}

func GetLanguageConfig(language string) (LanguageConfig, bool) {
	config, ok := languageConfigs[language]
	return config, ok
}
