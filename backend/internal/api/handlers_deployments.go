package api

import (
	"encoding/base64"
	"net/http"
	"os"
	"strconv"
	"time"

	"servercli/internal/model"
	"servercli/internal/service"
)

// deploymentBootstrapScriptPath is the operator bootstrap script served
// unauthenticated at /deployment-bootstrap.sh (relative to the control plane
// working directory). It contains only the interactive OSS sync/verify flow
// and never any credentials, so it is safe to expose publicly.
const deploymentBootstrapScriptPath = "scripts/deployment-bootstrap.sh"

// ossProfileView is the API representation of an OSS profile. The encrypted
// access key material is write-only and must never appear in any response.
type ossProfileView struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Endpoint       string     `json:"endpoint"`
	Region         string     `json:"region"`
	Bucket         string     `json:"bucket"`
	Prefix         string     `json:"prefix"`
	IsPrivate      bool       `json:"is_private"`
	LastTestedAt   *time.Time `json:"last_tested_at"`
	LastTestResult string     `json:"last_test_result,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func toOSSProfileView(p *model.OSSProfile) ossProfileView {
	return ossProfileView{
		ID:             p.ID,
		Name:           p.Name,
		Endpoint:       p.Endpoint,
		Region:         p.Region,
		Bucket:         p.Bucket,
		Prefix:         p.Prefix,
		IsPrivate:      p.IsPrivate,
		LastTestedAt:   p.LastTestedAt,
		LastTestResult: p.LastTestResult,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
	}
}

func toOSSProfileViews(ps []*model.OSSProfile) []ossProfileView {
	out := make([]ossProfileView, 0, len(ps))
	for _, p := range ps {
		out = append(out, toOSSProfileView(p))
	}
	return out
}

// deploymentActorID returns the acting admin identity from the request
// context. All deployment write handlers are wrapped by requireAdmin, so the
// admin is always present.
func (s *Server) deploymentActorID(r *http.Request) string {
	if admin := adminFrom(r.Context()); admin != nil {
		return admin.ID
	}
	return ""
}

// registerDeploymentRoutes wires the deployment management API.
func (s *Server) registerDeploymentRoutes(mux *http.ServeMux) {
	const group = "部署管理"

	// Features.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/deployments/features", Group: group, Auth: AuthAdminOrToken, Summary: "部署 Feature 列表", Debug: true}, s.adminOrToken(service.ResourceDeployments, service.ActionRead, "/api/v1/deployments/features")(s.handleListDeploymentFeatures))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/features", Group: group, Auth: AuthAdmin, Summary: "创建/注册部署 Feature", Body: `{"feature_key":"my-app","name":"My App","backup_mode":"none"}`, Errors: []string{"400", "409"}, Debug: true}, s.requireAdmin(s.handleCreateDeploymentFeature))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/deployments/features/{id}", Group: group, Auth: AuthAdminOrToken, Summary: "部署 Feature 详情", Debug: true}, s.adminOrToken(service.ResourceDeployments, service.ActionRead, "/api/v1/deployments/features/{id}")(s.handleGetDeploymentFeature))
	s.register(mux, RouteSpec{Method: "DELETE", Path: "/api/v1/deployments/features/{id}", Group: group, Auth: AuthAdmin, Summary: "删除部署 Feature", Errors: []string{"409"}, Debug: true}, s.requireAdmin(s.handleDeleteDeploymentFeature))
	s.register(mux, RouteSpec{Method: "PATCH", Path: "/api/v1/deployments/features/{id}", Group: group, Auth: AuthAdmin, Summary: "更新部署 Feature（backup_mode 等元数据）", Debug: true}, s.requireAdmin(s.handleUpdateDeploymentFeature))

	// Releases.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/deployments/releases", Group: group, Auth: AuthAdminOrToken, Summary: "部署 Release 列表", Params: []RouteParam{{Name: "feature_id", In: "query", Type: "string"}}, Debug: true}, s.adminOrToken(service.ResourceDeployments, service.ActionRead, "/api/v1/deployments/releases")(s.handleListDeploymentReleases))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/releases", Group: group, Auth: AuthAdmin, Summary: "发布部署 Release", Body: `{"feature_id":"...","version":"1.0.0","object_key":"..."}`, Errors: []string{"400", "404"}, Debug: true}, s.requireAdmin(s.handleCreateDeploymentRelease))

	// OSS profiles.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/deployments/oss-profiles", Group: group, Auth: AuthAdminOrToken, Summary: "OSS Profile 列表（不含凭据）", Debug: true}, s.adminOrToken(service.ResourceDeployments, service.ActionRead, "/api/v1/deployments/oss-profiles")(s.handleListOSSProfiles))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/oss-profiles", Group: group, Auth: AuthAdmin, Summary: "创建 OSS Profile（凭据只写不读）", Body: `{"name":"primary","endpoint":"oss-cn-hangzhou.aliyuncs.com","bucket":"my-bucket","access_key_id":"...","access_key_secret":"..."}`, Errors: []string{"400"}, Debug: true}, s.requireAdmin(s.handleCreateOSSProfile))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/oss-profiles/{id}/test", Group: group, Auth: AuthAdmin, Summary: "测试 OSS Profile 连通性/权限", Errors: []string{"400", "404"}, Debug: true}, s.requireAdmin(s.handleTestOSSProfile))
	s.register(mux, RouteSpec{Method: "PUT", Path: "/api/v1/deployments/oss-profiles/{id}", Group: group, Auth: AuthAdmin, Summary: "更新 OSS Profile（凭据留空表示不修改）", Errors: []string{"400", "404"}, Debug: true}, s.requireAdmin(s.handleUpdateOSSProfile))
	s.register(mux, RouteSpec{Method: "DELETE", Path: "/api/v1/deployments/oss-profiles/{id}", Group: group, Auth: AuthAdmin, Summary: "删除 OSS Profile", Errors: []string{"409"}, Debug: true}, s.requireAdmin(s.handleDeleteOSSProfile))

	// Repository sync.
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/repository/sync", Group: group, Auth: AuthAdmin, Summary: "触发仓库同步与校验", Errors: []string{"400", "404"}, Debug: true}, s.requireAdmin(s.handleRepositorySync))

	// Config profiles.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/deployments/config-profiles", Group: group, Auth: AuthAdminOrToken, Summary: "配置 Profile 列表", Params: []RouteParam{{Name: "scope_type", In: "query", Type: "string"}, {Name: "scope_id", In: "query", Type: "string"}}, Debug: true}, s.adminOrToken(service.ResourceDeployments, service.ActionRead, "/api/v1/deployments/config-profiles")(s.handleListConfigProfiles))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/config-profiles", Group: group, Auth: AuthAdmin, Summary: "创建配置 Profile", Body: `{"name":"prod","scope_type":"shared","content_yaml":"..."}`, Errors: []string{"400"}, Debug: true}, s.requireAdmin(s.handleCreateConfigProfile))
	s.register(mux, RouteSpec{Method: "PUT", Path: "/api/v1/deployments/config-profiles/{id}", Group: group, Auth: AuthAdmin, Summary: "更新配置 Profile", Errors: []string{"400", "404"}, Debug: true}, s.requireAdmin(s.handleUpdateConfigProfile))
	s.register(mux, RouteSpec{Method: "DELETE", Path: "/api/v1/deployments/config-profiles/{id}", Group: group, Auth: AuthAdmin, Summary: "删除配置 Profile", Debug: true}, s.requireAdmin(s.handleDeleteConfigProfile))

	// Targets.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/deployments/targets", Group: group, Auth: AuthAdminOrToken, Summary: "部署目标列表", Debug: true}, s.adminOrToken(service.ResourceDeployments, service.ActionRead, "/api/v1/deployments/targets")(s.handleListDeploymentTargets))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/targets", Group: group, Auth: AuthAdmin, Summary: "创建部署目标", Body: `{"feature_id":"...","node_id":"..."}`, Errors: []string{"400", "404"}, Debug: true}, s.requireAdmin(s.handleCreateDeploymentTarget))
	s.register(mux, RouteSpec{Method: "PUT", Path: "/api/v1/deployments/targets/{id}", Group: group, Auth: AuthAdmin, Summary: "更新部署目标", Errors: []string{"400", "404"}, Debug: true}, s.requireAdmin(s.handleUpdateDeploymentTarget))
	s.register(mux, RouteSpec{Method: "DELETE", Path: "/api/v1/deployments/targets/{id}", Group: group, Auth: AuthAdmin, Summary: "移除部署目标", Errors: []string{"409"}, Debug: true}, s.requireAdmin(s.handleDeleteDeploymentTarget))

	// Secret references.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/deployments/secrets/references", Group: group, Auth: AuthAdminOrToken, Summary: "Secret 引用列表（无正文）", Params: []RouteParam{{Name: "feature_id", In: "query", Type: "string"}, {Name: "scope_type", In: "query", Type: "string"}, {Name: "scope_id", In: "query", Type: "string"}}, Debug: true}, s.adminOrToken(service.ResourceDeploymentSecrets, service.ActionManage, "/api/v1/deployments/secrets/references")(s.handleListSecretReferences))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/secrets/references", Group: group, Auth: AuthAdmin, Summary: "创建 Secret 引用（元数据，无正文）", Body: `{"name":"db","feature_id":"...","scope_type":"shared"}`, Errors: []string{"400", "404", "409"}, Debug: true}, s.requireAdmin(s.handleCreateSecretReference))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/secrets/{id}/overwrite", Group: group, Auth: AuthAdmin, Summary: "覆盖 Secret（只写不读）", Body: `{"value":"...","reason":"..."}`, Errors: []string{"400", "404"}, Debug: true}, s.requireAdmin(s.handleOverwriteSecret))

	// Operations.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/deployments/operations", Group: group, Auth: AuthAdminOrToken, Summary: "部署操作列表", Params: []RouteParam{{Name: "limit", In: "query", Type: "integer"}}, Debug: true}, s.adminOrToken(service.ResourceDeployments, service.ActionRead, "/api/v1/deployments/operations")(s.handleListOperations))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/operations", Group: group, Auth: AuthAdmin, Summary: "创建部署操作", Body: `{"action":"install","feature_id":"...","release_id":"...","target_ids":["..."]}`, Errors: []string{"400", "404", "409"}, Debug: true}, s.requireAdmin(s.handleCreateOperation))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/deployments/operations/{id}", Group: group, Auth: AuthAdminOrToken, Summary: "部署操作详情（含目标与步骤）", Debug: true}, s.adminOrToken(service.ResourceDeployments, service.ActionRead, "/api/v1/deployments/operations/{id}")(s.handleGetOperationDetail))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/operations/{id}/cancel", Group: group, Auth: AuthAdmin, Summary: "取消部署操作", Body: `{"reason":"..."}`, Errors: []string{"400", "404", "409"}, Debug: true}, s.requireAdmin(s.handleCancelOperation))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/operations/{id}/continue", Group: group, Auth: AuthAdmin, Summary: "继续执行失败的部署操作", Errors: []string{"400", "404", "409"}, Debug: true}, s.requireAdmin(s.handleContinueOperation))

	// Backups.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/deployments/backups", Group: group, Auth: AuthAdminOrToken, Summary: "备份列表", Params: []RouteParam{{Name: "feature_id", In: "query", Type: "string"}, {Name: "node_id", In: "query", Type: "string"}, {Name: "limit", In: "query", Type: "integer"}}, Debug: true}, s.adminOrToken(service.ResourceDeployments, service.ActionRead, "/api/v1/deployments/backups")(s.handleListBackups))
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/deployments/backups/{id}", Group: group, Auth: AuthAdminOrToken, Summary: "备份详情", Debug: true}, s.adminOrToken(service.ResourceDeployments, service.ActionRead, "/api/v1/deployments/backups/{id}")(s.handleGetBackup))

	// Bootstrap sessions.
	s.register(mux, RouteSpec{Method: "GET", Path: "/api/v1/deployments/bootstrap-sessions", Group: group, Auth: AuthAdminOrToken, Summary: "节点引导会话列表", Debug: true}, s.adminOrToken(service.ResourceBootstrapSessions, service.ActionRead, "/api/v1/deployments/bootstrap-sessions")(s.handleListBootstrapSessions))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/bootstrap-sessions", Group: group, Auth: AuthAdmin, Summary: "创建节点引导会话（返回一次性 token）", Body: `{"node_id":"...","bucket":"...","prefix":"..."}`, Errors: []string{"400", "404"}, Debug: true}, s.requireAdmin(s.handleCreateBootstrapSession))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/deployments/bootstrap-sessions/{id}/revoke", Group: group, Auth: AuthAdmin, Summary: "撤销节点引导会话", Errors: []string{"400", "404"}, Debug: true}, s.requireAdmin(s.handleRevokeBootstrapSession))

	// Agent endpoints.
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/agent/deployments/upload-authorize", Group: "Agent", Auth: AuthAgent, Summary: "节点上传授权（精确前缀）", Body: `{"operation_id":"...","target_id":"...","feature_key":"..."}`, Errors: []string{"401", "403"}, Debug: true}, s.agentAuth(s.handleAgentUploadAuthorize))
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/agent/deployments/bootstrap/report", Group: "Agent", Auth: AuthNone, Summary: "节点引导状态上报（一次性 token 认证）", Body: `{"session_token":"...","state":"repository_syncing","message":"..."}`, Errors: []string{"400", "403", "404"}, Debug: true}, s.handleAgentBootstrapReport)
	s.register(mux, RouteSpec{Method: "POST", Path: "/api/v1/agent/deployments/bootstrap/materialize", Group: "Agent", Auth: AuthNone, Summary: "节点引导签名密钥物化（一次性 token 认证，走 HTTPS）", Body: `{"session_token":"..."}`, Errors: []string{"400", "403", "404"}, Debug: true}, s.handleAgentBootstrapMaterialize)

	// Public bootstrap script (no auth; contains no credentials).
	s.register(mux, RouteSpec{Method: "GET", Path: "/deployment-bootstrap.sh", Group: group, Auth: AuthNone, Summary: "节点引导脚本（公开，无凭证）", Debug: false}, s.handleDeploymentBootstrapScript)
}

// ─── Features ────────────────────────────────────────────────────────────

func (s *Server) handleListDeploymentFeatures(w http.ResponseWriter, r *http.Request) {
	features, err := s.deployments.ListFeatures(r.Context())
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"features": features})
}

func (s *Server) handleCreateDeploymentFeature(w http.ResponseWriter, r *http.Request) {
	var in service.CreateFeatureInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	f, err := s.deployments.CreateFeature(r.Context(), s.deploymentActorID(r), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"feature": f})
}

func (s *Server) handleGetDeploymentFeature(w http.ResponseWriter, r *http.Request) {
	f, err := s.deployments.GetFeature(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feature": f})
}

func (s *Server) handleUpdateDeploymentFeature(w http.ResponseWriter, r *http.Request) {
	var in service.CreateFeatureInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	f, err := s.deployments.UpdateFeature(r.Context(), s.deploymentActorID(r), r.PathValue("id"), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feature": f})
}

func (s *Server) handleDeleteDeploymentFeature(w http.ResponseWriter, r *http.Request) {
	if err := s.deployments.DeleteFeature(r.Context(), s.deploymentActorID(r), r.PathValue("id")); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Releases ────────────────────────────────────────────────────────────

func (s *Server) handleListDeploymentReleases(w http.ResponseWriter, r *http.Request) {
	releases, err := s.deployments.ListReleases(r.Context(), r.URL.Query().Get("feature_id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": releases})
}

func (s *Server) handleCreateDeploymentRelease(w http.ResponseWriter, r *http.Request) {
	var in service.CreateReleaseInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	rel, err := s.deployments.CreateRelease(r.Context(), s.deploymentActorID(r), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"release": rel})
}

// ─── OSS Profiles ────────────────────────────────────────────────────────

func (s *Server) handleListOSSProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.deployments.ListOSSProfiles(r.Context())
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": toOSSProfileViews(profiles)})
}

func (s *Server) handleCreateOSSProfile(w http.ResponseWriter, r *http.Request) {
	var in service.OSSProfileInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	p, err := s.deployments.CreateOSSProfile(r.Context(), s.deploymentActorID(r), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"profile": toOSSProfileView(p)})
}

func (s *Server) handleTestOSSProfile(w http.ResponseWriter, r *http.Request) {
	ok, message, err := s.deployments.TestOSSProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "message": message})
}

func (s *Server) handleUpdateOSSProfile(w http.ResponseWriter, r *http.Request) {
	var in service.OSSProfileInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	p, err := s.deployments.UpdateOSSProfile(r.Context(), s.deploymentActorID(r), r.PathValue("id"), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": toOSSProfileView(p)})
}

func (s *Server) handleDeleteOSSProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.deployments.DeleteOSSProfile(r.Context(), s.deploymentActorID(r), r.PathValue("id")); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Repository sync ─────────────────────────────────────────────────────

func (s *Server) handleRepositorySync(w http.ResponseWriter, r *http.Request) {
	res, err := s.deployments.RepositorySync(r.Context(), s.deploymentActorID(r))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sync": map[string]any{
		"started_at": res.StartedAt,
		"status":     res.Status,
	}})
}

// ─── Config profiles ─────────────────────────────────────────────────────

func (s *Server) handleListConfigProfiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	profiles, err := s.deployments.ListConfigProfiles(r.Context(), q.Get("scope_type"), q.Get("scope_id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": profiles})
}

func (s *Server) handleCreateConfigProfile(w http.ResponseWriter, r *http.Request) {
	var in service.ConfigProfileInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	p, err := s.deployments.CreateConfigProfile(r.Context(), s.deploymentActorID(r), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"profile": p})
}

func (s *Server) handleUpdateConfigProfile(w http.ResponseWriter, r *http.Request) {
	var in service.ConfigProfileInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	p, err := s.deployments.UpdateConfigProfile(r.Context(), s.deploymentActorID(r), r.PathValue("id"), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": p})
}

func (s *Server) handleDeleteConfigProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.deployments.DeleteConfigProfile(r.Context(), s.deploymentActorID(r), r.PathValue("id")); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Targets ─────────────────────────────────────────────────────────────

func (s *Server) handleListDeploymentTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := s.deployments.ListTargets(r.Context())
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": targets})
}

func (s *Server) handleCreateDeploymentTarget(w http.ResponseWriter, r *http.Request) {
	var in service.TargetInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	t, err := s.deployments.CreateTarget(r.Context(), s.deploymentActorID(r), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"target": t})
}

func (s *Server) handleUpdateDeploymentTarget(w http.ResponseWriter, r *http.Request) {
	var in service.TargetInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	t, err := s.deployments.UpdateTarget(r.Context(), s.deploymentActorID(r), r.PathValue("id"), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"target": t})
}

func (s *Server) handleDeleteDeploymentTarget(w http.ResponseWriter, r *http.Request) {
	if err := s.deployments.DeleteTarget(r.Context(), s.deploymentActorID(r), r.PathValue("id")); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Secret references ───────────────────────────────────────────────────

func (s *Server) handleListSecretReferences(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	refs, err := s.deployments.ListSecretReferences(r.Context(), q.Get("feature_id"), q.Get("scope_type"), q.Get("scope_id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": refs})
}

func (s *Server) handleCreateSecretReference(w http.ResponseWriter, r *http.Request) {
	var in service.SecretReferenceInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	ref, err := s.deployments.CreateSecretReference(r.Context(), s.deploymentActorID(r), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"secret": ref})
}

func (s *Server) handleOverwriteSecret(w http.ResponseWriter, r *http.Request) {
	var in service.OverwriteSecretInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	ref, err := s.deployments.OverwriteSecret(r.Context(), s.deploymentActorID(r), r.PathValue("id"), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": ref})
}

// ─── Operations ──────────────────────────────────────────────────────────

func (s *Server) handleListOperations(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ops, err := s.deployments.ListOperations(r.Context(), limit)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operations": ops})
}

func (s *Server) handleCreateOperation(w http.ResponseWriter, r *http.Request) {
	var in service.CreateOperationInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	op, err := s.deployments.CreateOperation(r.Context(), s.deploymentActorID(r), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"operation": op})
}

func (s *Server) handleGetOperationDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := s.deployments.GetOperationDetail(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"operation": detail.Operation,
		"targets":   detail.Targets,
		"steps":     detail.Steps,
	})
}

func (s *Server) handleCancelOperation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	op, err := s.deployments.CancelOperation(r.Context(), s.deploymentActorID(r), r.PathValue("id"), in.Reason)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operation": op})
}

func (s *Server) handleContinueOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.deployments.ContinueOperation(r.Context(), s.deploymentActorID(r), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operation": op})
}

// ─── Backups ─────────────────────────────────────────────────────────────

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	backups, err := s.deployments.ListBackups(r.Context(), q.Get("feature_id"), q.Get("node_id"), limit)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": backups})
}

func (s *Server) handleGetBackup(w http.ResponseWriter, r *http.Request) {
	b, err := s.deployments.GetBackup(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backup": b})
}

// ─── Bootstrap sessions ──────────────────────────────────────────────────

func (s *Server) handleListBootstrapSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.deployments.ListBootstrapSessions(r.Context())
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (s *Server) handleCreateBootstrapSession(w http.ResponseWriter, r *http.Request) {
	var in service.BootstrapSessionInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	res, err := s.deployments.CreateBootstrapSession(r.Context(), s.deploymentActorID(r), in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"session": res.Session,
		"command": res.Command,
		"token":   res.Token,
	})
}

func (s *Server) handleRevokeBootstrapSession(w http.ResponseWriter, r *http.Request) {
	if err := s.deployments.RevokeBootstrapSession(r.Context(), s.deploymentActorID(r), r.PathValue("id")); err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Agent endpoints ─────────────────────────────────────────────────────

func (s *Server) handleAgentUploadAuthorize(w http.ResponseWriter, r *http.Request) {
	var in service.AgentUploadAuthorizeInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	node := nodeFrom(r.Context())
	auth, err := s.deployments.AgentUploadAuthorize(r.Context(), node.ID, in)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"endpoint":         auth.Endpoint,
		"bucket":           auth.Bucket,
		"prefix":           auth.Prefix,
		"credentials_type": auth.CredentialsType,
	})
}

func (s *Server) handleAgentBootstrapReport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionToken string `json:"session_token"`
		State        string `json:"state"`
		Message      string `json:"message"`
	}
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	sess, err := s.deployments.ReportBootstrapStatus(r.Context(), in.SessionToken, in.State, in.Message)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "session": sess})
}

func (s *Server) handleAgentBootstrapMaterialize(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionToken string `json:"session_token"`
	}
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	key, err := s.deployments.MaterializeDeploymentSigningKey(r.Context(), in.SessionToken)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"signing_key": base64.StdEncoding.EncodeToString(key)})
}

// ─── Public bootstrap script ─────────────────────────────────────────────

func (s *Server) handleDeploymentBootstrapScript(w http.ResponseWriter, r *http.Request) {
	body, err := os.ReadFile(deploymentBootstrapScriptPath)
	if err != nil {
		writeError(w, r, s.log, http.StatusNotFound, "NOT_FOUND", "bootstrap script not found", nil)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
