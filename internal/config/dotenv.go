package config

import (
	"bufio"
	"os"
	"strings"
)

// LoadDotEnv reads key=value pairs from the given file paths (defaulting to ".env")
// and populates any environment variables that are not already set in the process environment.
// Missing files are ignored.
func LoadDotEnv(paths ...string) error {
	if len(paths) == 0 {
		paths = []string{".env"}
	}
	for _, p := range paths {
		if err := loadSingleDotEnv(p); err != nil {
			return err
		}
	}
	return nil
}

func loadSingleDotEnv(path string) (retErr error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil && retErr == nil {
			retErr = closeErr
		}
	}()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimSpace(line[len("export"):])
		}
		eqIdx := strings.IndexByte(line, '=')
		if eqIdx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eqIdx])
		if key == "" {
			continue
		}
		rawVal := strings.TrimSpace(line[eqIdx+1:])
		val := parseDotEnvValue(rawVal)

		if existing, ok := os.LookupEnv(key); !ok || strings.TrimSpace(existing) == "" {
			if setErr := os.Setenv(key, val); setErr != nil {
				return setErr
			}
		}
	}
	return scanner.Err()
}

func parseDotEnvValue(raw string) string {
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		return raw[1 : len(raw)-1]
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		inner := raw[1 : len(raw)-1]
		var b strings.Builder
		b.Grow(len(inner))
		for i := 0; i < len(inner); i++ {
			if inner[i] == '\\' && i+1 < len(inner) {
				i++
				switch inner[i] {
				case 'n':
					b.WriteByte('\n')
				case 'r':
					b.WriteByte('\r')
				case 't':
					b.WriteByte('\t')
				case '"':
					b.WriteByte('"')
				case '\\':
					b.WriteByte('\\')
				default:
					b.WriteByte('\\')
					b.WriteByte(inner[i])
				}
				continue
			}
			b.WriteByte(inner[i])
		}
		return b.String()
	}

	// For unquoted values, strip trailing inline comments preceded by whitespace.
	for i := 0; i < len(raw); i++ {
		if raw[i] == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
			return strings.TrimSpace(raw[:i])
		}
	}
	return strings.TrimSpace(raw)
}
