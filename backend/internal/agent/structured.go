package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"servercli/internal/opsv2"
)

// reservedStructuredKey is the argument key the control plane uses to signal a
// structured Operation V2 task. When present, the executor writes the
// operation context to a 0600 JSON file and every secret to an independent
// 0600 file under a 0700 temp directory, then passes their paths through
// SERVERCLI_OPERATION_CONTEXT / SERVERCLI_SECRET_DIR. Nothing secret is ever
// placed in argv.
const reservedStructuredKey = "operation_request"

// structuredSecretsKey is the optional argument holding a secrets map.
const structuredSecretsKey = "_secrets"

// structuredEnv holds the env additions for a structured task.
type structuredEnv struct {
	contextPath string
	secretDir   string
	extra       []string
}

// prepareStructuredTask inspects payload arguments for a structured Operation
// V2 request. It returns nil when the task is a legacy positional task.
// On success the caller must invoke cleanup() after the process exits.
func prepareStructuredTask(payload *TaskPayload) (*structuredEnv, error) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(payload.Arguments, &args); err != nil {
		return nil, nil // legacy positional payload
	}
	rawOp, ok := args[reservedStructuredKey]
	if !ok {
		return nil, nil
	}
	var req opsv2.OperationRequest
	if err := json.Unmarshal(rawOp, &req); err != nil {
		return nil, fmt.Errorf("structured task: parse operation_request: %w", err)
	}
	if err := req.Validate(); err != nil {
		return nil, fmt.Errorf("structured task: invalid operation request: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "servercli-agent-op-*")
	if err != nil {
		return nil, fmt.Errorf("structured task: create temp dir: %w", err)
	}
	// Temp dir must be root-only readable; individual secret files are 0600.
	if err := os.Chmod(tmpDir, 0o700); err != nil {
		os.RemoveAll(tmpDir)
		return nil, err
	}

	// Write context file (0600) — the operation request, never secret values.
	ctxPath, err := opsv2.WriteAgentContext(tmpDir, &req, payload.TaskID)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("structured task: write context: %w", err)
	}

	// Write secret files (each 0600) when provided.
	secretDir := ""
	secrets := map[string]string{}
	if rawSecrets, ok := args[structuredSecretsKey]; ok {
		if err := json.Unmarshal(rawSecrets, &secrets); err != nil {
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("structured task: parse secrets: %w", err)
		}
	}
	if len(secrets) > 0 {
		secretDir, err = opsv2.WriteAgentSecrets(tmpDir, secrets)
		if err != nil {
			opsv2.CleanupAgentFiles(ctxPath, "")
			os.RemoveAll(tmpDir)
			return nil, fmt.Errorf("structured task: write secrets: %w", err)
		}
	}

	extra := []string{
		"SERVERCLI_OPERATION_CONTEXT=" + ctxPath,
	}
	if secretDir != "" {
		extra = append(extra, "SERVERCLI_SECRET_DIR="+secretDir)
	}
	return &structuredEnv{contextPath: ctxPath, secretDir: secretDir, extra: extra}, nil
}

// cleanup removes the context file, secret files and the temp directory.
func (s *structuredEnv) cleanup() {
	if s == nil {
		return
	}
	opsv2.CleanupAgentFiles(s.contextPath, s.secretDir)
	if s.contextPath != "" {
		dir := filepath.Dir(s.contextPath)
		if strings.HasPrefix(filepath.Base(dir), "servercli-agent-op-") {
			os.RemoveAll(dir)
		}
	}
}
