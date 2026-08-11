package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"servercli/internal/model"
)

func TestDeclarativeClusterCRUD(t *testing.T) {
	env := setupAPI(t)

	status, body := env.serve(http.MethodPost, "/api/v1/clusters", map[string]any{
		"name": "production", "environment": "prod", "primary_epoch": 7,
		"release_channel": "stable",
		"update_policy":   map[string]any{"auto_apply": true, "maintenance": false},
	}, env.adminHeaders())
	if status != http.StatusCreated {
		t.Fatalf("create cluster status=%d body=%s", status, body)
	}
	var created struct {
		Cluster struct {
			ID             string             `json:"id"`
			Name           string             `json:"name"`
			Environment    string             `json:"environment"`
			PrimaryEpoch   int64              `json:"primary_epoch"`
			ReleaseChannel string             `json:"release_channel"`
			UpdatePolicy   model.UpdatePolicy `json:"update_policy"`
		} `json:"cluster"`
	}
	mustUnmarshalDeclarative(t, body, &created)
	if created.Cluster.ID == "" || created.Cluster.Name != "production" || created.Cluster.PrimaryEpoch != 7 || !created.Cluster.UpdatePolicy.AutoApply {
		t.Fatalf("unexpected created cluster: %+v", created.Cluster)
	}
	id := created.Cluster.ID

	status, body = env.serve(http.MethodGet, "/api/v1/clusters?environment=prod", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("list clusters status=%d body=%s", status, body)
	}
	var listed struct {
		Clusters []model.Cluster `json:"clusters"`
	}
	mustUnmarshalDeclarative(t, body, &listed)
	if len(listed.Clusters) != 1 || listed.Clusters[0].ID != id {
		t.Fatalf("unexpected clusters: %+v", listed.Clusters)
	}

	status, body = env.serve(http.MethodGet, "/api/v1/clusters/"+id, nil, env.adminHeaders())
	if status != http.StatusOK || !strings.Contains(string(body), `"release_channel":"stable"`) {
		t.Fatalf("get cluster status=%d body=%s", status, body)
	}

	status, body = env.serve(http.MethodPatch, "/api/v1/clusters/"+id, map[string]any{
		"name": "production-renamed", "status": model.ClusterStatusDegraded,
	}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("patch cluster status=%d body=%s", status, body)
	}
	var patched struct {
		Cluster model.Cluster `json:"cluster"`
	}
	mustUnmarshalDeclarative(t, body, &patched)
	if patched.Cluster.Name != "production-renamed" || patched.Cluster.Status != model.ClusterStatusDegraded {
		t.Fatalf("unexpected patched cluster: %+v", patched.Cluster)
	}

	status, body = env.serve(http.MethodDelete, "/api/v1/clusters/"+id, nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("delete cluster status=%d body=%s", status, body)
	}
	status, body = env.serve(http.MethodGet, "/api/v1/clusters/"+id, nil, env.adminHeaders())
	if status != http.StatusNotFound {
		t.Fatalf("get deleted cluster status=%d body=%s", status, body)
	}
}

func TestDeclarativeProfileModulesRoundTrip(t *testing.T) {
	env := setupAPI(t)
	clusterID := createDeclarativeTestCluster(t, env, "profiles", "test", 3)
	modules := []map[string]any{
		{
			"module_id": "docker", "version": "27.1", "config": map[string]string{"data_root": "/srv/docker"},
			"secret_refs":  []map[string]any{{"key": "registry_token", "store": "env"}},
			"dependencies": []string{"network"}, "risk_level": model.RiskMedium,
		},
	}
	status, body := env.serve(http.MethodPost, "/api/v1/clusters/"+clusterID+"/profiles", map[string]any{
		"name": "standard", "version": "2", "modules": modules,
	}, env.adminHeaders())
	if status != http.StatusCreated {
		t.Fatalf("create profile status=%d body=%s", status, body)
	}
	var created struct {
		Profile struct {
			ID      string                `json:"id"`
			Modules []model.ProfileModule `json:"modules"`
		} `json:"profile"`
	}
	mustUnmarshalDeclarative(t, body, &created)
	if created.Profile.ID == "" || len(created.Profile.Modules) != 1 || created.Profile.Modules[0].ModuleID != "docker" || created.Profile.Modules[0].Config["data_root"] != "/srv/docker" {
		t.Fatalf("unexpected profile response: %+v", created.Profile)
	}
	if strings.Contains(string(body), "modules_json") {
		t.Fatalf("raw modules_json leaked: %s", body)
	}

	status, body = env.serve(http.MethodGet, "/api/v1/profiles/"+created.Profile.ID, nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("get profile status=%d body=%s", status, body)
	}
	var got struct {
		Profile struct {
			Modules []model.ProfileModule `json:"modules"`
		} `json:"profile"`
	}
	mustUnmarshalDeclarative(t, body, &got)
	if len(got.Profile.Modules) != 1 || got.Profile.Modules[0].SecretRefs[0].Key != "registry_token" {
		t.Fatalf("modules did not round trip: %+v", got.Profile.Modules)
	}

	status, body = env.serve(http.MethodGet, "/api/v1/clusters/"+clusterID+"/profiles", nil, env.adminHeaders())
	if status != http.StatusOK || !strings.Contains(string(body), `"module_id":"docker"`) {
		t.Fatalf("list profiles status=%d body=%s", status, body)
	}
}

func TestDeclarativeNodeAddressesRoundTripAndFilter(t *testing.T) {
	env := setupAPI(t)
	clusterID := createDeclarativeTestCluster(t, env, "nodes", "test", 4)
	addresses := []map[string]any{
		{"address": "10.2.0.8", "address_type": "private", "port": 9047, "preferred": true},
		{"address": "203.0.113.8", "address_type": "public", "port": 9047},
	}
	status, body := env.serve(http.MethodPost, "/api/v1/declarative-nodes", map[string]any{
		"cluster_id": clusterID, "node_id": "node-declarative-1", "role": "child",
		"lifecycle": model.NodeLifecycleReady, "addresses": addresses,
	}, env.adminHeaders())
	if status != http.StatusCreated {
		t.Fatalf("create declarative node status=%d body=%s", status, body)
	}
	var created struct {
		Node struct {
			ID        string                         `json:"id"`
			ClusterID string                         `json:"cluster_id"`
			Addresses []model.DeclarativeNodeAddress `json:"addresses"`
		} `json:"declarative_node"`
	}
	mustUnmarshalDeclarative(t, body, &created)
	if created.Node.ID == "" || created.Node.ClusterID != clusterID || len(created.Node.Addresses) != 2 || !created.Node.Addresses[0].Preferred {
		t.Fatalf("unexpected declarative node: %+v", created.Node)
	}
	if strings.Contains(string(body), "addresses_json") {
		t.Fatalf("raw addresses_json leaked: %s", body)
	}

	status, body = env.serve(http.MethodGet, fmt.Sprintf("/api/v1/declarative-nodes?cluster_id=%s&lifecycle=%s", clusterID, model.NodeLifecycleReady), nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("list declarative nodes status=%d body=%s", status, body)
	}
	var listed struct {
		Nodes []struct {
			ID        string                         `json:"id"`
			Addresses []model.DeclarativeNodeAddress `json:"addresses"`
		} `json:"declarative_nodes"`
	}
	mustUnmarshalDeclarative(t, body, &listed)
	if len(listed.Nodes) != 1 || listed.Nodes[0].ID != created.Node.ID || len(listed.Nodes[0].Addresses) != 2 {
		t.Fatalf("unexpected filtered nodes: %+v", listed.Nodes)
	}

	status, body = env.serve(http.MethodGet, fmt.Sprintf("/api/v1/declarative-nodes?cluster_id=%s&lifecycle=%s", clusterID, model.NodeLifecycleRetired), nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("list empty declarative nodes status=%d body=%s", status, body)
	}
	mustUnmarshalDeclarative(t, body, &listed)
	if len(listed.Nodes) != 0 {
		t.Fatalf("lifecycle filter returned unexpected nodes: %+v", listed.Nodes)
	}
}

func TestOperationV2CreateIdempotencyTransitionsAndSecretScrubbing(t *testing.T) {
	env := setupAPI(t)
	clusterID := createDeclarativeTestCluster(t, env, "operations", "prod", 9)
	const fakeSecret = "fake-secret-value-DO-NOT-LEAK"
	request := map[string]any{
		"operation_id": "operation-stable-1", "operation_type": model.OpTypeUpdate,
		"cluster_id": clusterID, "node_id": "node-op-1", "module_id": "docker",
		"service_instance_id": "docker-main", "desired_revision": "rev-42",
		"arguments": map[string]any{"registry_password": fakeSecret, "nested": map[string]any{"token": fakeSecret}},
		"approval":  "pending", "risk_level": model.RiskHigh, "idempotency_key": "operation-idem-1",
		"deadline": nil, "primary_epoch": 9,
	}
	headers := declarativeHeaders(env.adminHeaders(), "Idempotency-Key", "operation-idem-1")
	status, body := env.serve(http.MethodPost, "/api/v1/operations", request, headers)
	if status != http.StatusCreated {
		t.Fatalf("create operation status=%d body=%s", status, body)
	}
	var created struct {
		Operation operationView `json:"operation"`
		Created   bool          `json:"created"`
	}
	mustUnmarshalDeclarative(t, body, &created)
	if !created.Created || created.Operation.ID == "" || created.Operation.Status != model.OpStatusPlanned {
		t.Fatalf("unexpected operation create response: %+v", created)
	}
	assertOperationSecretsScrubbed(t, body, fakeSecret)
	opID := created.Operation.ID

	status, body = env.serve(http.MethodPost, "/api/v1/operations", request, headers)
	if status != http.StatusOK {
		t.Fatalf("idempotent replay status=%d body=%s", status, body)
	}
	var replay struct {
		Operation operationView `json:"operation"`
		Created   bool          `json:"created"`
	}
	mustUnmarshalDeclarative(t, body, &replay)
	if replay.Created || replay.Operation.ID != opID {
		t.Fatalf("unexpected replay response: %+v", replay)
	}
	assertOperationSecretsScrubbed(t, body, fakeSecret)

	strictRequest := cloneDeclarativeMap(t, request)
	strictRequest["unexpected"] = "rejected"
	strictRequest["idempotency_key"] = "operation-idem-strict"
	strictHeaders := declarativeHeaders(env.adminHeaders(), "Idempotency-Key", "operation-idem-strict")
	status, body = env.serve(http.MethodPost, "/api/v1/operations", strictRequest, strictHeaders)
	if status != http.StatusBadRequest {
		t.Fatalf("strict operation parse status=%d body=%s", status, body)
	}

	status, body = env.serve(http.MethodGet, "/api/v1/operations?cluster_id="+clusterID+"&status=planned&limit=10&offset=0", nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("list operations status=%d body=%s", status, body)
	}
	var listed struct {
		Operations []operationView `json:"operations"`
	}
	mustUnmarshalDeclarative(t, body, &listed)
	if len(listed.Operations) != 1 || listed.Operations[0].ID != opID {
		t.Fatalf("unexpected operation list: %+v", listed.Operations)
	}
	assertOperationSecretsScrubbed(t, body, fakeSecret)

	status, body = env.serve(http.MethodGet, "/api/v1/operations/"+opID, nil, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("get operation status=%d body=%s", status, body)
	}
	assertOperationSecretsScrubbed(t, body, fakeSecret)
	if !strings.Contains(string(body), `"steps":[]`) {
		t.Fatalf("operation detail did not include steps: %s", body)
	}

	status, body = env.serve(http.MethodPost, "/api/v1/operations/"+opID+"/approve", nil, env.adminHeaders())
	if status != http.StatusOK || !strings.Contains(string(body), `"status":"queued"`) {
		t.Fatalf("approve operation status=%d body=%s", status, body)
	}
	for _, next := range []string{model.OpStatusDispatched, model.OpStatusRunning, model.OpStatusVerifying, model.OpStatusSucceeded} {
		status, body = env.serve(http.MethodPost, "/api/v1/operations/"+opID+"/status", map[string]any{"status": next}, env.adminHeaders())
		if status != http.StatusOK || !strings.Contains(string(body), `"status":"`+next+`"`) {
			t.Fatalf("transition to %s status=%d body=%s", next, status, body)
		}
		assertOperationSecretsScrubbed(t, body, fakeSecret)
	}
	status, body = env.serve(http.MethodPost, "/api/v1/operations/"+opID+"/status", map[string]any{"status": model.OpStatusRunning}, env.adminHeaders())
	if status < 400 || status >= 500 {
		t.Fatalf("invalid transition status=%d body=%s", status, body)
	}
}

func TestReleaseCacheRegisterAndMarkAvailable(t *testing.T) {
	env := setupAPI(t)
	status, body := env.serve(http.MethodPost, "/api/v1/release-cache", map[string]any{
		"version": "0.0.40", "source_repository": "owner/repo", "source_release": "v0.0.40",
		"os": "linux", "arch": "amd64", "artifact_name": "servercli-linux-amd64.tar.gz",
		"artifact_size": 12345, "sha256": strings.Repeat("a", 64), "oss_key": "releases/v0.0.40/servercli.tar.gz",
	}, env.adminHeaders())
	if status != http.StatusCreated {
		t.Fatalf("register release cache status=%d body=%s", status, body)
	}
	var created struct {
		Entry model.ReleaseCacheEntry `json:"release_cache_entry"`
	}
	mustUnmarshalDeclarative(t, body, &created)
	if created.Entry.ID == "" || created.Entry.Status != model.ReleaseCachePending {
		t.Fatalf("unexpected release cache entry: %+v", created.Entry)
	}

	status, body = env.serve(http.MethodPost, "/api/v1/release-cache/"+created.Entry.ID+"/mark-available", map[string]any{"verified": true}, env.adminHeaders())
	if status != http.StatusOK {
		t.Fatalf("mark release cache available status=%d body=%s", status, body)
	}
	var marked struct {
		Entry model.ReleaseCacheEntry `json:"release_cache_entry"`
	}
	mustUnmarshalDeclarative(t, body, &marked)
	if marked.Entry.Status != model.ReleaseCacheAvailable || marked.Entry.UploadedAt == nil || marked.Entry.VerifiedAt == nil {
		t.Fatalf("unexpected marked release cache entry: %+v", marked.Entry)
	}

	status, body = env.serve(http.MethodGet, "/api/v1/release-cache?version=0.0.40&status=available", nil, env.adminHeaders())
	if status != http.StatusOK || !strings.Contains(string(body), created.Entry.ID) {
		t.Fatalf("list release cache status=%d body=%s", status, body)
	}
}

func createDeclarativeTestCluster(t *testing.T, env *testEnv, name, environment string, epoch int64) string {
	t.Helper()
	status, body := env.serve(http.MethodPost, "/api/v1/clusters", map[string]any{
		"name": name, "environment": environment, "primary_epoch": epoch,
	}, env.adminHeaders())
	if status != http.StatusCreated {
		t.Fatalf("create test cluster status=%d body=%s", status, body)
	}
	var response struct {
		Cluster model.Cluster `json:"cluster"`
	}
	mustUnmarshalDeclarative(t, body, &response)
	if response.Cluster.ID == "" {
		t.Fatalf("create test cluster returned no id: %s", body)
	}
	return response.Cluster.ID
}

func assertOperationSecretsScrubbed(t *testing.T, body []byte, fakeSecret string) {
	t.Helper()
	text := strings.ToLower(string(body))
	if strings.Contains(string(body), fakeSecret) {
		t.Fatalf("operation response leaked secret value: %s", body)
	}
	for _, forbidden := range []string{"arguments_json", "idempotency_key"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("operation response leaked %s: %s", forbidden, body)
		}
	}
}

func declarativeHeaders(base map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(base)+1)
	for k, v := range base {
		out[k] = v
	}
	out[key] = value
	return out
}

func cloneDeclarativeMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func mustUnmarshalDeclarative(t *testing.T, data []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode response: %v body=%s", err, data)
	}
}
