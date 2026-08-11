package opsv2

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// EnvOperationContext points agents at the 0600 structured request file.
	EnvOperationContext = "SERVERCLI_OPERATION_CONTEXT"
	// EnvSecretDir points agents at the 0700 directory containing one 0600
	// file per secret.
	EnvSecretDir = "SERVERCLI_SECRET_DIR"
)

// AgentInput describes the filesystem contract presented to an agent.
type AgentInput struct {
	Operation   OperationRequest `json:"operation"`
	SecretDir   string           `json:"secret_dir"`
	ContextPath string           `json:"context_path"`
}

var safeFilenamePart = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// WriteAgentContext writes a secret-free structured request to a private JSON
// file. opID is used only to make the filename recognizable and is sanitized
// before use.
func WriteAgentContext(dir string, req *OperationRequest, opID string) (contextPath string, err error) {
	if req == nil {
		return "", errors.New("operation request is nil")
	}
	if err := req.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(opID) == "" {
		return "", errors.New("opID is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create agent context directory: %w", err)
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal agent context: %w", err)
	}
	prefix := safeFilenamePart.ReplaceAllString(opID, "_")
	if prefix == "" || prefix == "." || prefix == ".." {
		prefix = "operation"
	}
	f, err := os.CreateTemp(dir, prefix+"-*.json")
	if err != nil {
		return "", fmt.Errorf("create agent context: %w", err)
	}
	path := f.Name()
	keep := false
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close agent context: %w", closeErr)
		}
		if !keep || err != nil {
			_ = os.Remove(path)
		}
	}()

	if err = f.Chmod(0o600); err != nil {
		return "", fmt.Errorf("chmod agent context: %w", err)
	}
	if _, err = f.Write(payload); err != nil {
		return "", fmt.Errorf("write agent context: %w", err)
	}
	if err = f.Sync(); err != nil {
		return "", fmt.Errorf("sync agent context: %w", err)
	}
	keep = true
	return path, nil
}

// WriteAgentSecrets creates a private temporary directory and stores each
// secret in an independent private file. Secret names must already be safe
// single path components; values are never logged or included in errors.
func WriteAgentSecrets(dir string, secrets map[string]string) (secretDir string, err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create secret parent directory: %w", err)
	}
	secretDir, err = os.MkdirTemp(dir, "servercli-secrets-*")
	if err != nil {
		return "", fmt.Errorf("create agent secret directory: %w", err)
	}
	keep := false
	defer func() {
		if !keep || err != nil {
			_ = os.RemoveAll(secretDir)
		}
	}()
	if err := os.Chmod(secretDir, 0o700); err != nil {
		return "", fmt.Errorf("chmod agent secret directory: %w", err)
	}

	for key, value := range secrets {
		if err := validateSecretFilename(key); err != nil {
			return "", err
		}
		path := filepath.Join(secretDir, key)
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			return "", fmt.Errorf("write agent secret %q: %w", key, err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return "", fmt.Errorf("chmod agent secret %q: %w", key, err)
		}
	}
	keep = true
	return secretDir, nil
}

func validateSecretFilename(key string) error {
	if key == "" || key == "." || key == ".." || filepath.Base(key) != key || strings.ContainsAny(key, `/\\`) {
		return fmt.Errorf("invalid secret file name %q", key)
	}
	return nil
}

// CleanupAgentFiles removes both parts of the agent filesystem contract.
func CleanupAgentFiles(contextPath, secretDir string) {
	if contextPath != "" {
		_ = os.Remove(contextPath)
	}
	if secretDir != "" {
		_ = os.RemoveAll(secretDir)
	}
}

// LoadAgentContext strictly loads an OperationRequest and annotates it with
// the context path and secret directory supplied through the agent environment.
func LoadAgentContext(path string) (*AgentInput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent context: %w", err)
	}
	req, err := ParseOperationRequest(data)
	if err != nil {
		return nil, fmt.Errorf("load agent context: %w", err)
	}
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("validate agent context: %w", err)
	}
	return &AgentInput{
		Operation:   *req,
		SecretDir:   os.Getenv(EnvSecretDir),
		ContextPath: path,
	}, nil
}
