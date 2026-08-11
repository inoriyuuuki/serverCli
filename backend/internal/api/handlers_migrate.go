package api

import (
	"errors"
	"net/http"

	"servercli/internal/model"
	"servercli/internal/service"
)

// Command ID the node agent advertises for ServerCLI ops (see
// commands/servercli-ops.yaml). The control plane dispatches adopt/update/
// backup/restore to nodes through this command.
const migrateCommandID = "servercli-ops"

// adoptPlanSteps is the fixed per-service migration flow (requirement §10).
var adoptPlanSteps = []string{
	"只读发现（legacy 入口 / 容器 / 端口 / 数据库 / 版本）",
	"差异计划（旧入口 -> servercli ops 的映射与不清理项）",
	"冻结旧 cron/timer/install/update/backup/restore 入口",
	"等待旧任务结束",
	"创建迁移备份（含配置与数据目录摘要）",
	"校验目录/容器/端口/数据库/版本一致性",
	"写入 ownership 元数据 + 导入配置与 Secret 引用",
	"健康检查 -> owner 切换 servercli -> 禁用旧入口",
}

// adoptRedlines are the hard constraints shown in the plan.
var adoptRedlines = []string{
	"同一服务不能被旧 init 与 ServerCLI 同时操作（服务级锁）",
	"adopt 失败自动回滚 legacy owner，原数据不移动/删除/重建",
	"每项迁移后观察 1 更新周期 + 2 备份周期 + 恢复测试，再迁下一项",
	"Gitea 旧实例保持 MariaDB，不隐式迁移 PostgreSQL",
}

type migrateServiceView struct {
	NodeID      string `json:"node_id"`
	NodeName    string `json:"node_name,omitempty"`
	Service     string `json:"service"`
	Owner       string `json:"owner"`
	Environment string `json:"environment,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

func (s *Server) handleMigrateServices(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("node_id")
	rows, err := s.store.ListServiceOwnership(r.Context(), nodeID)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	names := s.nodeDisplayNames(r.Context(), ownershipNodeIDs(rows))
	out := make([]migrateServiceView, 0, len(rows))
	for _, row := range rows {
		out = append(out, migrateServiceView{
			NodeID: row.NodeID, NodeName: names[row.NodeID], Service: row.Service,
			Owner: row.Owner, Environment: row.Environment, UpdatedAt: row.UpdatedAt,
		})
	}
	if out == nil {
		out = []migrateServiceView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": out})
}

func ownershipNodeIDs(rows []model.ServiceOwnership) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if !seen[r.NodeID] {
			seen[r.NodeID] = true
			out = append(out, r.NodeID)
		}
	}
	return out
}

func (s *Server) handleMigratePlan(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	nodeID := q.Get("node_id")
	svc := q.Get("service")
	if svc == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "service is required", nil)
		return
	}
	owner := "legacy-init"
	if nodeID != "" {
		rows, err := s.store.ListServiceOwnership(r.Context(), nodeID)
		if err == nil {
			for _, row := range rows {
				if row.Service == svc {
					owner = row.Owner
					break
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id":       nodeID,
		"service":       svc,
		"current_owner": owner,
		"steps":         adoptPlanSteps,
		"redlines":      adoptRedlines,
		"dry_run":       true,
	})
}

type migrateOpsInput struct {
	NodeID    string `json:"node_id"`
	Service   string `json:"service"`
	Operation string `json:"operation"` // adopt | update | backup | restore
	BackupID  string `json:"backup_id,omitempty"`
	Confirm   bool   `json:"confirm"`
}

func (s *Server) handleMigrateOps(w http.ResponseWriter, r *http.Request) {
	var in migrateOpsInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if in.NodeID == "" || in.Service == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "node_id and service are required", nil)
		return
	}
	if !in.Confirm {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "confirm=true is required for migration ops", nil)
		return
	}
	switch in.Operation {
	case "adopt", "update", "backup":
	case "restore":
		if in.BackupID == "" {
			writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "restore requires backup_id", nil)
			return
		}
	default:
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "unsupported operation", nil)
		return
	}

	idem := r.Header.Get("Idempotency-Key")
	if idem == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "Idempotency-Key header required", nil)
		return
	}

	// Resolve the command version the node actually advertises.
	cmds, err := s.store.NodeCommands(r.Context(), in.NodeID)
	if err != nil {
		writeServiceError(w, r, s.log, err)
		return
	}
	version := ""
	for _, c := range cmds {
		if c.CommandID == migrateCommandID {
			version = c.CommandVersion
			break
		}
	}
	if version == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST",
			"node has not advertised the servercli-ops command (need 0.0.33+ deployed)", nil)
		return
	}

	args := map[string]any{
		"operation": in.Operation,
		"service":   in.Service,
		"confirm":   true,
	}
	if in.BackupID != "" {
		args["backup_id"] = in.BackupID
	}

	requestedBy, actorType := "", ""
	if admin := adminFrom(r.Context()); admin != nil && admin.ID != "" {
		requestedBy, actorType = admin.ID, model.ActorAdmin
	} else if tp := tokenPrincipalFrom(r.Context()); tp != nil {
		requestedBy, actorType = tp.TokenID, model.ActorAI
	} else {
		writeError(w, r, s.log, http.StatusUnauthorized, "UNAUTHENTICATED", "missing principal", nil)
		return
	}

	t, err := s.tasks.CreateTask(r.Context(), in.NodeID, requestedBy, actorType, idem, service.CreateTaskInput{
		CommandID:      migrateCommandID,
		CommandVersion: version,
		Arguments:      args,
		TimeoutSeconds: 1800,
	})
	if err != nil {
		if errors.Is(err, service.ErrOffline) || errors.Is(err, service.ErrNotFound) {
			writeServiceError(w, r, s.log, err)
			return
		}
		writeServiceError(w, r, s.log, err)
		return
	}
	s.auditor.OK(r.Context(), service.AuditInput{
		ActorType: actorType, ActorID: requestedBy, Action: "migrate." + in.Operation,
		ResourceType: "node", ResourceID: in.NodeID,
		Summary: "migration ops dispatched: " + in.Operation + " " + in.Service,
	})
	s.events.publishEvent("", EventTasksChanged)
	writeJSON(w, http.StatusCreated, map[string]any{"task": map[string]any{
		"id": t.ID, "status": t.Status, "node_id": t.NodeID,
		"operation": in.Operation, "service": in.Service, "created_at": t.QueuedAt,
	}})
}
