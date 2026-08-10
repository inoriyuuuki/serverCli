package secretstore

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Strict dotenv rules (documented in the module design):
//   - no variable expansion, no command substitution;
//   - no duplicate keys, no illegal key names;
//   - values may not contain control characters or NUL;
//   - shell never source/eval secret files (handled by consumers);
//   - ordinary single-line values are passed via environment variables;
//   - multi-line / binary / cert / key material is delivered via 0600 temp
//     files under /run and deleted after the operation (see modman).

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseDotEnv parses a strict env file into a map. It rejects expansion,
// substitution, duplicates and invalid keys.
func ParseDotEnv(data []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsAny(line, "`$") {
			return nil, fmt.Errorf("line %d: variable expansion / command substitution is forbidden", lineNo)
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", lineNo)
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if !keyRe.MatchString(key) {
			return nil, fmt.Errorf("line %d: illegal key %q", lineNo, key)
		}
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("line %d: duplicate key %q", lineNo, key)
		}
		// Strip optional surrounding quotes.
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		for _, r := range val {
			if r == 0 || (r < 0x20 && r != '\t') || r == 0x7f {
				return nil, fmt.Errorf("line %d: control character in value", lineNo)
			}
		}
		out[key] = val
	}
	return out, sc.Err()
}

// ReadDotEnvFile reads a strict dotenv file from disk.
func ReadDotEnvFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseDotEnv(raw)
}
