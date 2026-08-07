package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"servercli/internal/model"
)

const nodeColumns = `id, environment_id, instance_name, alias, role, hostname, status, enabled,
	agent_version, app_version, os_name, os_version, arch, frontend_port, backend_port,
	last_heartbeat_at, last_online_at, labels_json, metadata_json, credential_hash, credential_prefix,
	credential_version, created_at, updated_at`

func scanNode(row interface{ Scan(...any) error }) (*model.Node, error) {
	var n model.Node
	var alias, hostname, agentVer, appVer, osName, osVer, arch, labels, metadata, credHash, credPrefix sql.NullString
	var lhb, loa, created, updated sql.NullString
	var enabled int64
	var fport, bport, credVer int64
	if err := row.Scan(&n.ID, &n.EnvironmentID, &n.InstanceName, &alias, &n.Role, &hostname, &n.Status, &enabled,
		&agentVer, &appVer, &osName, &osVer, &arch, &fport, &bport,
		&lhb, &loa, &labels, &metadata, &credHash, &credPrefix, &credVer, &created, &updated); err != nil {
		return nil, err
	}
	n.Alias = alias.String
	n.Hostname = hostname.String
	n.AgentVersion = agentVer.String
	n.AppVersion = appVer.String
	n.OSName = osName.String
	n.OSVersion = osVer.String
	n.Arch = arch.String
	n.LabelsJSON = labels.String
	n.MetadataJSON = metadata.String
	n.CredentialHash = credHash.String
	n.CredentialPrefix = credPrefix.String
	n.Enabled = parseBool(enabled)
	n.FrontendPort = int(fport)
	n.BackendPort = int(bport)
	n.CredentialVersion = int(credVer)
	var err error
	if n.LastHeartbeatAt, err = parseTime(lhb); err != nil {
		return nil, err
	}
	if n.LastOnlineAt, err = parseTime(loa); err != nil {
		return nil, err
	}
	if n.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if n.UpdatedAt, err = parseTimeVal(updated); err != nil {
		return nil, err
	}
	return &n, nil
}

// CreateNode inserts a node.
func (s *Store) CreateNode(ctx context.Context, n *model.Node) error {
	n.CreatedAt = now()
	n.UpdatedAt = n.CreatedAt
	_, err := s.db.ExecContext(ctx, `INSERT INTO node
		(id, environment_id, instance_name, alias, role, hostname, status, enabled,
		 agent_version, app_version, os_name, os_version, arch, frontend_port, backend_port,
		 last_heartbeat_at, last_online_at, labels_json, metadata_json, credential_hash, credential_prefix,
		 credential_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)`,
		n.ID, n.EnvironmentID, n.InstanceName, n.Alias, n.Role, n.Hostname, n.Status, boolInt(n.Enabled),
		n.AgentVersion, n.AppVersion, n.OSName, n.OSVersion, n.Arch, n.FrontendPort, n.BackendPort,
		nullTime(n.LastHeartbeatAt), nullTime(n.LastOnlineAt), n.LabelsJSON, n.MetadataJSON, n.CredentialHash, n.CredentialPrefix,
		n.CredentialVersion, ts(n.CreatedAt), ts(n.UpdatedAt))
	return err
}

// NodeByID finds a node.
func (s *Store) NodeByID(ctx context.Context, id string) (*model.Node, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM node WHERE id = $1`, id)
	n, err := scanNode(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return n, nil
}

// NodeByCredentialHash finds a node by credential hash.
func (s *Store) NodeByCredentialHash(ctx context.Context, hash string) (*model.Node, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM node WHERE credential_hash = $1`, hash)
	n, err := scanNode(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return n, nil
}

// NodeByInstanceName finds a node by instance name within an environment.
func (s *Store) NodeByInstanceName(ctx context.Context, envID, name string) (*model.Node, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM node WHERE environment_id = $1 AND instance_name = $2`, envID, name)
	n, err := scanNode(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return n, nil
}

// NodeByAlias finds a node by alias within an environment.
func (s *Store) NodeByAlias(ctx context.Context, envID, alias string) (*model.Node, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM node WHERE environment_id = $1 AND alias = $2`, envID, alias)
	n, err := scanNode(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return n, nil
}

// ListNodes returns nodes with optional filters, newest first.
func (s *Store) ListNodes(ctx context.Context, role, status string, enabled *bool, keyword string, limit, offset int) ([]*model.Node, error) {
	q := `SELECT ` + nodeColumns + ` FROM node`
	conds := []string{}
	args := []any{}
	if role != "" {
		args = append(args, role)
		conds = append(conds, `role = $`+strconv.Itoa(len(args)))
	}
	if status != "" {
		args = append(args, status)
		conds = append(conds, `status = $`+strconv.Itoa(len(args)))
	}
	if enabled != nil {
		args = append(args, boolInt(*enabled))
		conds = append(conds, `enabled = $`+strconv.Itoa(len(args)))
	}
	if keyword != "" {
		args = append(args, "%"+keyword+"%")
		conds = append(conds, `(instance_name LIKE $`+strconv.Itoa(len(args))+` OR alias LIKE $`+strconv.Itoa(len(args))+` OR hostname LIKE $`+strconv.Itoa(len(args))+` OR id LIKE $`+strconv.Itoa(len(args))+`)`)
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY created_at ASC`
	if limit > 0 {
		args = append(args, limit)
		q += ` LIMIT $` + strconv.Itoa(len(args))
	}
	if offset > 0 {
		args = append(args, offset)
		q += ` OFFSET $` + strconv.Itoa(len(args))
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UpdateNode persists mutable node fields.
func (s *Store) UpdateNode(ctx context.Context, n *model.Node) error {
	n.UpdatedAt = now()
	_, err := s.db.ExecContext(ctx, `UPDATE node SET
		alias=$1, hostname=$2, status=$3, enabled=$4, agent_version=$5, app_version=$6,
		os_name=$7, os_version=$8, arch=$9, frontend_port=$10, backend_port=$11,
		last_heartbeat_at=$12, last_online_at=$13, labels_json=$14, metadata_json=$15,
		credential_hash=$16, credential_prefix=$17, credential_version=$18, updated_at=$19
		WHERE id=$20`,
		n.Alias, n.Hostname, n.Status, boolInt(n.Enabled), n.AgentVersion, n.AppVersion,
		n.OSName, n.OSVersion, n.Arch, n.FrontendPort, n.BackendPort,
		nullTime(n.LastHeartbeatAt), nullTime(n.LastOnlineAt), n.LabelsJSON, n.MetadataJSON,
		n.CredentialHash, n.CredentialPrefix, n.CredentialVersion, ts(n.UpdatedAt), n.ID)
	return err
}

// CountEnabledPrimary returns the number of enabled primary nodes in an env.
func (s *Store) CountEnabledPrimary(ctx context.Context, envID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM node WHERE environment_id=$1 AND role='primary' AND enabled=1`, envID).Scan(&n)
	return n, err
}

// FindEnabledPrimary returns the enabled primary node of an environment.
func (s *Store) FindEnabledPrimary(ctx context.Context, envID string) (*model.Node, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM node WHERE environment_id=$1 AND role='primary' AND enabled=1 ORDER BY created_at ASC LIMIT 1`, envID)
	n, err := scanNode(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return n, nil
}

// MarkNodeOnline refreshes heartbeat and online timestamps.
func (s *Store) MarkNodeOnline(ctx context.Context, nodeID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE node SET last_heartbeat_at=$1, last_online_at=$1, status=$2, updated_at=$3
		WHERE id=$4 AND enabled=1`,
		ts(at), model.NodeStatusOnline, ts(now()), nodeID)
	return err
}

// MarkNodesOffline flips online/degraded nodes to offline when stale.
func (s *Store) MarkNodesOffline(ctx context.Context, envID string, before time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM node
		WHERE environment_id=$1 AND enabled=1 AND status IN ('online','degraded') AND (last_heartbeat_at IS NULL OR last_heartbeat_at < $2)`,
		envID, ts(before))
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		for _, id := range ids {
			if _, err := s.db.ExecContext(ctx, `UPDATE node SET status=$1, updated_at=$2 WHERE id=$3`, model.NodeStatusOffline, ts(now()), id); err != nil {
				return ids, err
			}
		}
	}
	return ids, nil
}

// ---- node_address ----

// ReplaceAddresses removes existing addresses for a node and inserts the new
// set in one transaction.
func (s *Store) ReplaceAddresses(ctx context.Context, nodeID string, addresses []*model.NodeAddress) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		tsx := s.Tx(tx)
		if _, err := tsx.exec(ctx, `DELETE FROM node_address WHERE node_id=$1`, nodeID); err != nil {
			return err
		}
		for _, a := range addresses {
			a.ID = model.NewUUID()
			a.NodeID = nodeID
			a.FirstSeenAt = now()
			a.LastSeenAt = a.FirstSeenAt
			if _, err := tsx.exec(ctx, `INSERT INTO node_address
				(id, node_id, address, address_type, service_port, first_seen_at, last_seen_at, is_preferred)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				a.ID, a.NodeID, a.Address, a.AddressType, a.ServicePort, ts(a.FirstSeenAt), ts(a.LastSeenAt), boolInt(a.IsPreferred)); err != nil {
				return err
			}
		}
		return nil
	})
}

// NodeAddresses lists addresses for a node.
func (s *Store) NodeAddresses(ctx context.Context, nodeID string) ([]*model.NodeAddress, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, address, address_type, service_port, first_seen_at, last_seen_at, is_preferred
		FROM node_address WHERE node_id=$1 ORDER BY is_preferred DESC, last_seen_at DESC`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.NodeAddress{}
	for rows.Next() {
		var a model.NodeAddress
		var fs, ls sql.NullString
		var pref int64
		var port int64
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Address, &a.AddressType, &port, &fs, &ls, &pref); err != nil {
			return nil, err
		}
		a.ServicePort = int(port)
		a.IsPreferred = parseBool(pref)
		if a.FirstSeenAt, err = parseTimeVal(fs); err != nil {
			return nil, err
		}
		if a.LastSeenAt, err = parseTimeVal(ls); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// FindNodeByAddress finds an enabled node whose address and service port match.
// Returns ErrConflict when multiple nodes match (caller must ask for a more
// specific selector).
func (s *Store) FindNodeByAddress(ctx context.Context, envID, address string, port int) (*model.Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.node_id FROM node_address a JOIN node n ON n.id=a.node_id
		WHERE a.address=$1 AND a.service_port=$2 AND n.environment_id=$3 AND n.enabled=1`, address, port, envID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrNotFound
	}
	if len(ids) > 1 {
		return nil, ErrConflict
	}
	return s.NodeByID(ctx, ids[0])
}

// ---- node_heartbeat ----

// CreateHeartbeat inserts a heartbeat row and updates node timestamps.
func (s *Store) CreateHeartbeat(ctx context.Context, hb *model.NodeHeartbeat) error {
	hb.ID = model.NewUUID()
	hb.RecordedAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO node_heartbeat
		(id, node_id, recorded_at, cpu_usage_percent, memory_total_bytes, memory_used_bytes,
		 disk_total_bytes, disk_used_bytes, load_1, load_5, load_15, uptime_seconds, time_offset_ms,
		 summary_json, is_protected, protected_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		hb.ID, hb.NodeID, ts(hb.RecordedAt), hb.CPUUsagePercent, hb.MemoryTotalBytes, hb.MemoryUsedBytes,
		hb.DiskTotalBytes, hb.DiskUsedBytes, hb.Load1, hb.Load5, hb.Load15, hb.UptimeSeconds, hb.TimeOffsetMS,
		hb.SummaryJSON, boolInt(hb.IsProtected), nullTime(hb.ProtectedAt))
	return err
}

// RecentHeartbeats returns the most recent heartbeats for a node.
func (s *Store) RecentHeartbeats(ctx context.Context, nodeID string, limit int) ([]*model.NodeHeartbeat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, node_id, recorded_at, cpu_usage_percent, memory_total_bytes, memory_used_bytes,
		disk_total_bytes, disk_used_bytes, load_1, load_5, load_15, uptime_seconds, time_offset_ms, summary_json, is_protected, protected_at
		FROM node_heartbeat WHERE node_id=$1 ORDER BY recorded_at DESC LIMIT $2`, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.NodeHeartbeat{}
	for rows.Next() {
		hb, err := scanHeartbeat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, hb)
	}
	return out, rows.Err()
}

func scanHeartbeat(row interface{ Scan(...any) error }) (*model.NodeHeartbeat, error) {
	var h model.NodeHeartbeat
	var rec, summary, prot sql.NullString
	var protFlag int64
	if err := row.Scan(&h.ID, &h.NodeID, &rec, &h.CPUUsagePercent, &h.MemoryTotalBytes, &h.MemoryUsedBytes,
		&h.DiskTotalBytes, &h.DiskUsedBytes, &h.Load1, &h.Load5, &h.Load15, &h.UptimeSeconds, &h.TimeOffsetMS,
		&summary, &protFlag, &prot); err != nil {
		return nil, err
	}
	h.SummaryJSON = summary.String
	h.IsProtected = parseBool(protFlag)
	var err error
	if h.RecordedAt, err = parseTimeVal(rec); err != nil {
		return nil, err
	}
	if h.ProtectedAt, err = parseTime(prot); err != nil {
		return nil, err
	}
	return &h, nil
}

// ---- node_command ----

const commandColumns = `id, node_id, command_id, command_version, capability_id, category, title, description,
	parameter_schema_json, permission_profile, timeout_seconds, max_output_bytes, enabled, manifest_hash, executable_hash, first_seen_at, last_seen_at`

func scanCommand(row interface{ Scan(...any) error }) (*model.NodeCommand, error) {
	var c model.NodeCommand
	var capID, category, title, desc, schemaJSON, manifestHash, exeHash sql.NullString
	var timeout int64
	var maxOut int64
	var enabled int64
	var fs, ls sql.NullString
	if err := row.Scan(&c.ID, &c.NodeID, &c.CommandID, &c.CommandVersion, &capID, &category, &title, &desc,
		&schemaJSON, &c.PermissionProfile, &timeout, &maxOut, &enabled, &manifestHash, &exeHash, &fs, &ls); err != nil {
		return nil, err
	}
	c.CapabilityID = capID.String
	c.Category = category.String
	c.Title = title.String
	c.Description = desc.String
	c.ParameterSchemaJSON = schemaJSON.String
	c.ManifestHash = manifestHash.String
	c.ExecutableHash = exeHash.String
	c.TimeoutSeconds = int(timeout)
	c.MaxOutputBytes = maxOut
	c.Enabled = parseBool(enabled)
	var err error
	if c.FirstSeenAt, err = parseTimeVal(fs); err != nil {
		return nil, err
	}
	if c.LastSeenAt, err = parseTimeVal(ls); err != nil {
		return nil, err
	}
	return &c, nil
}

// UpsertNodeCommand inserts or updates a node command record.
func (s *Store) UpsertNodeCommand(ctx context.Context, c *model.NodeCommand) error {
	c.LastSeenAt = now()
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		tsx := s.Tx(tx)
		var existing string
		err := tsx.queryRow(ctx, `SELECT id FROM node_command WHERE node_id=$1 AND command_id=$2 AND command_version=$3`,
			c.NodeID, c.CommandID, c.CommandVersion).Scan(&existing)
		if err == nil {
			c.ID = existing
			_, err = tsx.exec(ctx, `UPDATE node_command SET
				capability_id=$1, category=$2, title=$3, description=$4, parameter_schema_json=$5,
				permission_profile=$6, timeout_seconds=$7, max_output_bytes=$8, enabled=$9,
				manifest_hash=$10, executable_hash=$11, last_seen_at=$12
				WHERE id=$13`,
				c.CapabilityID, c.Category, c.Title, c.Description, c.ParameterSchemaJSON,
				c.PermissionProfile, c.TimeoutSeconds, c.MaxOutputBytes, boolInt(c.Enabled),
				c.ManifestHash, c.ExecutableHash, ts(c.LastSeenAt), c.ID)
			return err
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		c.ID = model.NewUUID()
		c.FirstSeenAt = now()
		_, err = tsx.exec(ctx, `INSERT INTO node_command
			(id, node_id, command_id, command_version, capability_id, category, title, description,
			 parameter_schema_json, permission_profile, timeout_seconds, max_output_bytes, enabled, manifest_hash, executable_hash, first_seen_at, last_seen_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
			c.ID, c.NodeID, c.CommandID, c.CommandVersion, c.CapabilityID, c.Category, c.Title, c.Description,
			c.ParameterSchemaJSON, c.PermissionProfile, c.TimeoutSeconds, c.MaxOutputBytes, boolInt(c.Enabled), c.ManifestHash, c.ExecutableHash, ts(c.FirstSeenAt), ts(c.LastSeenAt))
		return err
	})
}

// NodeCommands lists command records for a node.
func (s *Store) NodeCommands(ctx context.Context, nodeID string) ([]*model.NodeCommand, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+commandColumns+` FROM node_command WHERE node_id=$1 ORDER BY category, command_id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.NodeCommand{}
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NodeCommandByID finds a specific command record.
func (s *Store) NodeCommandByID(ctx context.Context, nodeID, commandID, version string) (*model.NodeCommand, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+commandColumns+` FROM node_command WHERE node_id=$1 AND command_id=$2 AND command_version=$3`,
		nodeID, commandID, version)
	c, err := scanCommand(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return c, nil
}

// DeleteCommandsNotIn removes commands for a node that are not in the given
// (command_id, version) pairs, i.e. snapshot replacement.
func (s *Store) DeleteCommandsNotIn(ctx context.Context, nodeID string, keep map[string]bool) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, command_id, command_version FROM node_command WHERE node_id=$1`, nodeID)
	if err != nil {
		return 0, err
	}
	toDelete := []string{}
	for rows.Next() {
		var id, cid, ver string
		if err := rows.Scan(&id, &cid, &ver); err != nil {
			rows.Close()
			return 0, err
		}
		key := cid + "\x00" + ver
		if !keep[key] {
			toDelete = append(toDelete, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var deleted int64
	for _, id := range toDelete {
		res, err := s.db.ExecContext(ctx, `DELETE FROM node_command WHERE id=$1`, id)
		if err != nil {
			return deleted, err
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	return deleted, nil
}

// SearchCommands finds commands across nodes with optional filters.
func (s *Store) SearchCommands(ctx context.Context, nodeID, category, keyword string, limit, offset int) ([]*model.NodeCommand, error) {
	q := `SELECT ` + commandColumns + ` FROM node_command`
	conds := []string{`enabled=1`}
	args := []any{}
	add := func(v string) string {
		args = append(args, v)
		return `$` + strconv.Itoa(len(args))
	}
	if nodeID != "" {
		conds = append(conds, `node_id=`+add(nodeID))
	}
	if category != "" {
		conds = append(conds, `category=`+add(category))
	}
	if keyword != "" {
		kw := "%" + keyword + "%"
		args = append(args, kw)
		conds = append(conds, `(title LIKE $`+strconv.Itoa(len(args))+` OR command_id LIKE $`+strconv.Itoa(len(args))+` OR description LIKE $`+strconv.Itoa(len(args))+`)`)
	}
	q += ` WHERE ` + strings.Join(conds, ` AND `)
	q += ` ORDER BY category, command_id`
	if limit > 0 {
		args = append(args, limit)
		q += ` LIMIT $` + strconv.Itoa(len(args))
	}
	if offset > 0 {
		args = append(args, offset)
		q += ` OFFSET $` + strconv.Itoa(len(args))
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.NodeCommand{}
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
