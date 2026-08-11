package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

type ossCredentials struct {
	AccessKeyID     string `json:"access_key_id"`
	AccessKeySecret string `json:"access_key_secret"`
}

func loadOSSCredentials(filePath string) (ossCredentials, error) {
	if filePath == "" {
		credentials := ossCredentials{
			AccessKeyID:     strings.TrimSpace(os.Getenv("OSS_ACCESS_KEY_ID")),
			AccessKeySecret: strings.TrimSpace(os.Getenv("OSS_ACCESS_KEY_SECRET")),
		}
		if credentials.AccessKeyID == "" || credentials.AccessKeySecret == "" {
			return ossCredentials{}, errors.New("OSS credentials must come from OSS_ACCESS_KEY_ID/OSS_ACCESS_KEY_SECRET or --oss-ak-file")
		}
		return credentials, nil
	}

	info, err := os.Lstat(filePath)
	if err != nil {
		return ossCredentials{}, fmt.Errorf("read OSS credential file metadata: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ossCredentials{}, errors.New("OSS credential file must be a regular file, not a symlink")
	}
	if info.Mode().Perm() != 0o600 {
		return ossCredentials{}, fmt.Errorf("OSS credential file %q must have mode 0600", filePath)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ossCredentials{}, fmt.Errorf("read OSS credential file: %w", err)
	}
	credentials := ossCredentials{}
	if json.Unmarshal(data, &credentials) != nil {
		values := make(map[string]string)
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				return ossCredentials{}, errors.New("OSS credential file must be JSON or KEY=VALUE lines")
			}
			values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
		if err := scanner.Err(); err != nil {
			return ossCredentials{}, fmt.Errorf("scan OSS credential file: %w", err)
		}
		credentials.AccessKeyID = values["OSS_ACCESS_KEY_ID"]
		credentials.AccessKeySecret = values["OSS_ACCESS_KEY_SECRET"]
	}
	credentials.AccessKeyID = strings.TrimSpace(credentials.AccessKeyID)
	credentials.AccessKeySecret = strings.TrimSpace(credentials.AccessKeySecret)
	if credentials.AccessKeyID == "" || credentials.AccessKeySecret == "" {
		return ossCredentials{}, errors.New("OSS credential file is missing access_key_id/access_key_secret")
	}
	return credentials, nil
}
