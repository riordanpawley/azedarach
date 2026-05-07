package daemon

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

const linearAPIKeyEnv = "LINEAR_API_KEY"

func resolveLinearAPIKey(repoDir string) string {
	if value := strings.TrimSpace(os.Getenv(linearAPIKeyEnv)); value != "" {
		return value
	}
	if strings.TrimSpace(repoDir) == "" {
		return ""
	}
	value, _ := readDotEnvValue(filepath.Join(repoDir, ".env.local"), linearAPIKeyEnv)
	return strings.TrimSpace(value)
}

func readDotEnvValue(path, key string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		return trimDotEnvValue(value), true
	}
	return "", false
}

func trimDotEnvValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 1 {
		quote := value[0]
		if quote == '\'' || quote == '"' {
			if end := strings.IndexByte(value[1:], quote); end >= 0 {
				return value[1 : end+1]
			}
		}
	}
	if before, _, ok := strings.Cut(value, " #"); ok {
		return strings.TrimSpace(before)
	}
	return value
}
