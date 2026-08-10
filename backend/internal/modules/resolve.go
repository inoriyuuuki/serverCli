package modules

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"

	"servercli/internal/bootstrap"
	"servercli/internal/modman"
)

// SecretReader is the minimal secret access surface the resolver needs. It is
// implemented by *secretstore.Store.
type SecretReader interface {
	Get(key string) (string, bool)
	Set(key string, value string) error
}

// ResolveModuleInputs maps the decrypted Inventory + Bootstrap Store secrets
// onto a module's declared config_fields / secret_fields (from its
// module.yaml). Generated secrets (agent key, claim token, control-plane
// internal token) are atomically persisted into the store before being
// returned so retries reuse the same version.
//
// The returned maps are keyed by the module's declared field names, exactly
// what modman.Runner turns into SERVERCLI_CFG_*/SERVERCLI_SEC_*.
func ResolveModuleInputs(mod *modman.ModuleManifest, inv *bootstrap.Inventory, store SecretReader, operationID string) (config, secrets map[string]string, err error) {
	config = map[string]string{}
	secrets = map[string]string{}
	moduleID := ""
	if mod != nil {
		moduleID = mod.ID
	}
	if inv != nil {
		config["ENVIRONMENT"] = inv.Environment
		config["NODE_NAME"] = inv.Node.Name
		config["ROLE"] = inv.Node.Role
		if inv.Network.Domain != "" {
			config["DOMAIN"] = inv.Network.Domain
		}
		if inv.Network.PublicIP != "" {
			config["PUBLIC_IP"] = inv.Network.PublicIP
		}
		if len(inv.Network.PrivateIPs) > 0 {
			config["PRIVATE_IP"] = inv.Network.PrivateIPs[0]
		}
	}
	if operationID != "" {
		config["TRANSACTION_ID"] = operationID
	}

	switch moduleID {
	case "v2ray":
		// v2ray enabled/disabled comes from the Inventory service assignment.
		enabled := false
		if inv != nil {
			_, enabled = inv.Services["v2ray"]
		}
		config["ENABLED"] = fmt.Sprintf("%t", enabled)
		config["SOCKS_PORT"] = "1080"
		config["HTTP_PORT"] = "8080"
	case "docker":
		config["VERSION"] = "latest"
	case "postgres":
		config["DB_NAME"] = firstNonEmpty("servercli")
		config["APP_USER"] = firstNonEmpty("servercli")
		config["PORT"] = "5432"
		config["DATA_DIR"] = bootstrap.DirVarPostgres
		config["IMAGE"] = "paradedb/paradedb:pg17"
	case "caddy":
		config["DATA_DIR"] = bootstrap.DirVarLibServerCLI + "/caddy"
		config["MAINTENANCE"] = "true" // two-phase: maintenance first, route switch after control-plane
	case "control-plane":
		config["DB_NAME"] = "servercli"
		config["DB_USER"] = "servercli"
		config["DB_HOST"] = "host.docker.internal"
		config["DB_PORT"] = "5432"
		config["DATA_DIR"] = bootstrap.DirVarLibServerCLI + "/control-plane"
		config["PORT"] = "9045"
	case "agent":
		config["SOCKET_DIR"] = bootstrap.DirRunBootstrap + "/agent"
		if inv.Network.Domain != "" {
			config["CP_ADDRESS"] = "https://" + inv.Network.Domain
		}
	case "gitea":
		config["BACKEND"] = "postgres"
		config["DB_HOST"] = "host.docker.internal"
		config["DB_PORT"] = "5432"
		config["DB_NAME"] = "gitea"
		config["DB_USER"] = "gitea"
		config["PORT"] = "3000"
		config["DATA_DIR"] = bootstrap.DirVarLibServerCLI + "/gitea"
	}

	if mod == nil {
		return config, secrets, nil
	}
	for _, f := range mod.SecretFields {
		if isGeneratedSecret(moduleID, f.Name) {
			v, gerr := generatedSecret(moduleID, f.Name, store)
			if gerr != nil {
				return nil, nil, gerr
			}
			secrets[f.Name] = v
			continue
		}
		v, found := lookupSecret(store, moduleID, f.Name)
		if !found {
			if f.Required {
				// Report only the field name, never a value.
				return nil, nil, fmt.Errorf("module %s: required secret field %s is not present in the bundle/store", moduleID, f.Name)
			}
			continue
		}
		secrets[f.Name] = v
	}
	return config, secrets, nil
}

// isGeneratedSecret lists secrets the system creates and persists itself.
func isGeneratedSecret(moduleID, field string) bool {
	switch moduleID {
	case "agent":
		return field == "AGENT_PRIVATE_KEY" || field == "CLAIM_TOKEN"
	case "control-plane":
		return field == "INTERNAL_TOKEN"
	}
	return false
}

// generatedSecret loads a generated secret from the store or creates it and
// persists atomically before returning (retries reuse the same version).
func generatedSecret(moduleID, field string, store SecretReader) (string, error) {
	key := moduleID + "." + strings.ToLower(field)
	if v, ok := store.Get(key); ok && v != "" {
		return v, nil
	}
	var v string
	switch {
	case moduleID == "agent" && field == "AGENT_PRIVATE_KEY":
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return "", err
		}
		der, _ := x509.MarshalPKCS8PrivateKey(priv)
		v = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	case moduleID == "agent" && field == "CLAIM_TOKEN":
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		v = hex.EncodeToString(b)
	case moduleID == "control-plane" && field == "INTERNAL_TOKEN":
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		v = base64.RawURLEncoding.EncodeToString(b)
	default:
		return "", fmt.Errorf("module %s: no generator for secret field %s", moduleID, field)
	}
	if err := store.Set(key, v); err != nil {
		return "", fmt.Errorf("persist generated secret %s: %w", key, err)
	}
	return v, nil
}

// lookupSecret resolves a secret field against the bundle store. Accepted key
// forms (canonical first): <module>.<FIELD>, <module>_<FIELD>,
// <module>.<field-lower>, <module>_<field-lower>, <field>, <field-lower],
// plus a few aliases.
func lookupSecret(store SecretReader, moduleID, field string) (string, bool) {
	lower := strings.ToLower(field)
	candidates := []string{
		moduleID + "." + field,
		moduleID + "_" + field,
		moduleID + "." + lower,
		moduleID + "_" + lower,
		field,
		lower,
	}
	switch {
	case moduleID == "postgres" && field == "APP_PASSWORD":
		candidates = append(candidates, "postgres.app_password", "db_password", "postgres.password", "app_password")
	case moduleID == "control-plane" && field == "DB_PASSWORD":
		candidates = append(candidates, "postgres.app_password", "db_password", "postgres.password")
	case moduleID == "gitea" && field == "DB_PASSWORD":
		candidates = append(candidates, "gitea.db_password", "db_password", "postgres.app_password")
	case moduleID == "gitea" && field == "ADMIN_PASSWORD":
		candidates = append(candidates, "gitea.admin_password", "admin_password")
	}
	for _, c := range candidates {
		if v, ok := store.Get(c); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
