// Package env loads simple KEY=VALUE files into os.Environ without
// overwriting existing variables. It supports `export` prefixes and
// quoted multi-line values. There are no external dependencies.
package env

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Load reads the named .env file and sets each KEY in the process
// environment unless it is already set. A missing file is not an error.
func Load(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // path is operator-supplied config, not user input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for k, v := range parse(string(data)) {
		if k == "" || os.Getenv(k) != "" {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	return nil
}

// LoadNearest walks upward from start (or the current working directory if
// start is empty), Load()ing the first `.env` found. Returns the path
// loaded (or "" if none found) and any read error.
func LoadNearest(start string) (string, error) {
	path, ok := findUp(start, ".env")
	if !ok {
		return "", nil
	}
	return path, Load(path)
}

func findUp(start, name string) (string, bool) {
	if start == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", false
		}
		start = wd
	}
	dir := filepath.Clean(start)
	for {
		path := filepath.Join(dir, name)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func parse(content string) map[string]string {
	out := make(map[string]string)
	lines := strings.Split(content, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))

		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			continue
		}
		if v == "" {
			out[k] = ""
			continue
		}

		// Multi-line quoted value support.
		if v[0] == '"' || v[0] == '\'' {
			quote := v[0]
			val := v[1:]
			for !strings.HasSuffix(val, string(quote)) {
				i++
				if i >= len(lines) {
					break
				}
				val += "\n" + lines[i]
			}
			val = strings.TrimSuffix(val, string(quote))
			out[k] = val
			continue
		}
		out[k] = v
	}
	return out
}
