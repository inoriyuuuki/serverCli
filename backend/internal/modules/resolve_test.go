package modules

import (
	"strings"
	"testing"

	"servercli/internal/bootstrap"
	"servercli/internal/modman"
)

type memStore struct{ m map[string]string }

func (s *memStore) Get(k string) (string, bool) { v, ok := s.m[k]; return v, ok }
func (s *memStore) Set(k, v string) error       { s.m[k] = v; return nil }

func TestResolveModuleInputsSecretsAndGenerated(t *testing.T) {
	store := &memStore{m: map[string]string{
		"postgres.app_password": "pw1",
	}}
	inv := &bootstrap.Inventory{
		Environment: "prod",
		Node:        bootstrap.InventoryNode{Name: "n1", Role: "primary"},
		Network:     bootstrap.InventoryNetwork{Domain: "sc.example.invalid", PublicIP: "203.0.113.1"},
	}
	postgres := &modman.ModuleManifest{
		ID: "postgres", Version: "1.0.0", Phase: modman.PhaseFoundationCore, Delivery: modman.DeliveryEnv,
		SecretFields: []modman.Field{{Name: "POSTGRES_SUPER_PASSWORD", Required: false}, {Name: "APP_PASSWORD", Required: true}},
	}
	cfg, sec, err := ResolveModuleInputs(postgres, inv, store, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if cfg["ENVIRONMENT"] != "prod" || cfg["NODE_NAME"] != "n1" || cfg["DOMAIN"] != "sc.example.invalid" {
		t.Fatalf("config = %v", cfg)
	}
	if sec["APP_PASSWORD"] != "pw1" {
		t.Fatalf("APP_PASSWORD not resolved: %v", sec)
	}
	if _, ok := sec["POSTGRES_SUPER_PASSWORD"]; ok {
		t.Fatal("missing super password should not be silently present")
	}
	// Missing required secret -> error naming only the field.
	store2 := &memStore{m: map[string]string{}}
	_, _, err = ResolveModuleInputs(postgres, inv, store2, "")
	if err == nil || !strings.Contains(err.Error(), "APP_PASSWORD") {
		t.Fatalf("expected missing-field error, got %v", err)
	}
	if strings.Contains(err.Error(), "pw1") {
		t.Fatal("error must not contain secret values")
	}
}

func TestResolveModuleInputsGeneratedSecretsPersisted(t *testing.T) {
	store := &memStore{m: map[string]string{}}
	inv := &bootstrap.Inventory{Environment: "prod", Node: bootstrap.InventoryNode{Name: "n1", Role: "primary"}}
	agent := &modman.ModuleManifest{
		ID: "agent", Version: "1.0.0", Phase: modman.PhaseFoundationCore, Delivery: modman.DeliveryFile,
		SecretFields: []modman.Field{{Name: "AGENT_PRIVATE_KEY", Type: "file", Required: true}, {Name: "CLAIM_TOKEN", Required: true}},
	}
	_, sec, err := ResolveModuleInputs(agent, inv, store, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sec["AGENT_PRIVATE_KEY"], "PRIVATE KEY") {
		t.Fatalf("agent key not PEM: %.40s", sec["AGENT_PRIVATE_KEY"])
	}
	if len(sec["CLAIM_TOKEN"]) < 32 {
		t.Fatalf("claim token too short")
	}
	// Retry must reuse the same version (persisted).
	_, sec2, err := ResolveModuleInputs(agent, inv, store, "op-2")
	if err != nil {
		t.Fatal(err)
	}
	if sec2["CLAIM_TOKEN"] != sec["CLAIM_TOKEN"] {
		t.Fatal("generated secret changed across retries")
	}
}
