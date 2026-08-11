// Package store is the data access layer for ServerCLI's declarative ops V2
// entities. Same conventions as the rest of the store: TEXT RFC3339 UTC
// timestamps, INTEGER booleans, explicit SQL that runs on SQLite and
// PostgreSQL.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"servercli/internal/model"
)

const clusterColumns = `id, name, environment, active_primary_node_id, primary_epoch, release_channel,
	oss_provider_ref, update_policy_json, backup_policy_json, status, created_at, updated_at`

func scanCluster(row interface{ Scan(...any) error }) (*model.Cluster, error) {
	var c model.Cluster
	var epoch int64
	var created, updated sql.NullString
	var activePrimary, releaseChannel, ossRef, updatePolicy, backupPolicy sql.NullString
	if err := row.Scan(&c.ID, &c.Name, &c.Environment, &activePrimary, &epoch, &releaseChannel,
		&ossRef, &updatePolicy, &backupPolicy, &c.Status, &created, &updated); err != nil {
		return nil, err
	}
	c.ActivePrimaryNodeID = activePrimary.String
	c.ReleaseChannel = releaseChannel.String
	c.OSSProviderRef = ossRef.String
	c.UpdatePolicyJSON = updatePolicy.String
	c.BackupPolicyJSON = backupPolicy.String
	c.PrimaryEpoch = epoch
	var err error
	if c.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if c.UpdatedAt, err = parseTimeVal(updated); err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateCluster inserts a cluster.
func (s *Store) CreateCluster(ctx context.Context, c *model.Cluster) error {
	c.CreatedAt = now()
	c.UpdatedAt = now()
	if c.Status == "" {
		c.Status = model.ClusterStatusActive
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO cluster
		(id, name, environment, active_primary_node_id, primary_epoch, release_channel,
		 oss_provider_ref, update_policy_json, backup_policy_json, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		c.ID, c.Name, c.Environment, nullString(c.ActivePrimaryNodeID), c.PrimaryEpoch, nullString(c.ReleaseChannel),
		nullString(c.OSSProviderRef), c.UpdatePolicyJSON, c.BackupPolicyJSON, c.Status, ts(c.CreatedAt), ts(c.UpdatedAt))
	return err
}

// ClusterByID finds a cluster.
func (s *Store) ClusterByID(ctx context.Context, id string) (*model.Cluster, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+clusterColumns+` FROM cluster WHERE id=$1`, id)
	c, err := scanCluster(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return c, nil
}

// ListClusters lists clusters (optionally filtered by environment).
func (s *Store) ListClusters(ctx context.Context, environment string) ([]*model.Cluster, error) {
	q := `SELECT ` + clusterColumns + ` FROM cluster`
	args := []any{}
	if environment != "" {
		q += ` WHERE environment=$1`
		args = append(args, environment)
	}
	q += ` ORDER BY created_at`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.Cluster{}
	for rows.Next() {
		c, err := scanCluster(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateCluster updates mutable cluster fields.
func (s *Store) UpdateCluster(ctx context.Context, c *model.Cluster) error {
	c.UpdatedAt = now()
	_, err := s.db.ExecContext(ctx, `UPDATE cluster SET
		name=$2, environment=$3, active_primary_node_id=$4, primary_epoch=$5, release_channel=$6,
		oss_provider_ref=$7, update_policy_json=$8, backup_policy_json=$9, status=$10, updated_at=$11
		WHERE id=$1`,
		c.ID, c.Name, c.Environment, nullString(c.ActivePrimaryNodeID), c.PrimaryEpoch, nullString(c.ReleaseChannel),
		nullString(c.OSSProviderRef), c.UpdatePolicyJSON, c.BackupPolicyJSON, c.Status, ts(c.UpdatedAt))
	return err
}

// DeleteCluster removes a cluster (cascades are not automatic; callers gate).
func (s *Store) DeleteCluster(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM cluster WHERE id=$1`, id)
	if err != nil {
		return err
	}
	return rowsAffectedErr(res)
}

const profileColumns = `id, cluster_id, name, version, modules_json, default_config_json, secret_refs_json,
	backup_policy_json, update_policy_json, verification_policy_json, labels_json, resources_json,
	status, created_at, updated_at`

func scanProfile(row interface{ Scan(...any) error }) (*model.NodeProfile, error) {
	var p model.NodeProfile
	var created, updated sql.NullString
	if err := row.Scan(&p.ID, &p.ClusterID, &p.Name, &p.Version, &p.ModulesJSON, &p.DefaultConfigJSON,
		&p.SecretRefsJSON, &p.BackupPolicyJSON, &p.UpdatePolicyJSON, &p.VerificationPolicyJSON,
		&p.LabelsJSON, &p.ResourcesJSON, &p.Status, &created, &updated); err != nil {
		return nil, err
	}
	var err error
	if p.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if p.UpdatedAt, err = parseTimeVal(updated); err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateNodeProfile inserts a node profile.
func (s *Store) CreateNodeProfile(ctx context.Context, p *model.NodeProfile) error {
	p.CreatedAt = now()
	p.UpdatedAt = now()
	if p.Status == "" {
		p.Status = "active"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO node_profile
		(id, cluster_id, name, version, modules_json, default_config_json, secret_refs_json,
		 backup_policy_json, update_policy_json, verification_policy_json, labels_json, resources_json,
		 status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		p.ID, p.ClusterID, p.Name, p.Version, p.ModulesJSON, p.DefaultConfigJSON, p.SecretRefsJSON,
		p.BackupPolicyJSON, p.UpdatePolicyJSON, p.VerificationPolicyJSON, p.LabelsJSON, p.ResourcesJSON,
		p.Status, ts(p.CreatedAt), ts(p.UpdatedAt))
	return err
}

// NodeProfileByID finds a profile.
func (s *Store) NodeProfileByID(ctx context.Context, id string) (*model.NodeProfile, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+profileColumns+` FROM node_profile WHERE id=$1`, id)
	p, err := scanProfile(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return p, nil
}

// NodeProfileByName finds the latest version of a profile by name.
func (s *Store) NodeProfileByName(ctx context.Context, clusterID, name string) (*model.NodeProfile, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+profileColumns+` FROM node_profile
		WHERE cluster_id=$1 AND name=$2 ORDER BY version DESC LIMIT 1`, clusterID, name)
	p, err := scanProfile(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return p, nil
}

// ListNodeProfiles lists profiles for a cluster.
func (s *Store) ListNodeProfiles(ctx context.Context, clusterID string) ([]*model.NodeProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+profileColumns+` FROM node_profile
		WHERE cluster_id=$1 ORDER BY name, version`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.NodeProfile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateNodeProfile updates mutable profile fields.
func (s *Store) UpdateNodeProfile(ctx context.Context, p *model.NodeProfile) error {
	p.UpdatedAt = now()
	_, err := s.db.ExecContext(ctx, `UPDATE node_profile SET
		name=$3, version=$4, modules_json=$5, default_config_json=$6, secret_refs_json=$7,
		backup_policy_json=$8, update_policy_json=$9, verification_policy_json=$10, labels_json=$11,
		resources_json=$12, status=$13, updated_at=$14
		WHERE id=$1 AND cluster_id=$2`,
		p.ID, p.ClusterID, p.Name, p.Version, p.ModulesJSON, p.DefaultConfigJSON, p.SecretRefsJSON,
		p.BackupPolicyJSON, p.UpdatePolicyJSON, p.VerificationPolicyJSON, p.LabelsJSON, p.ResourcesJSON,
		p.Status, ts(p.UpdatedAt))
	return err
}

const declarativeNodeColumns = `id, cluster_id, node_id, role, profile_id, lifecycle, status, labels_json,
	addresses_json, os_name, os_version, arch, desired_revision, applied_revision, identity_generation,
	replacement_status, agent_status, legacy_mac, created_at, updated_at, retired_at`

func scanDeclarativeNode(row interface{ Scan(...any) error }) (*model.DeclarativeNode, error) {
	var n model.DeclarativeNode
	var created, updated, retired sql.NullString
	var profileID, osName, osVersion, arch, desiredRev, appliedRev, replacement, agentStatus, legacyMAC sql.NullString
	var gen int64
	if err := row.Scan(&n.ID, &n.ClusterID, &n.NodeID, &n.Role, &profileID, &n.Lifecycle, &n.Status,
		&n.LabelsJSON, &n.AddressesJSON, &osName, &osVersion, &arch, &desiredRev,
		&appliedRev, &gen, &replacement, &agentStatus, &legacyMAC, &created, &updated, &retired); err != nil {
		return nil, err
	}
	n.ProfileID = profileID.String
	n.OSName = osName.String
	n.OSVersion = osVersion.String
	n.Arch = arch.String
	n.DesiredRevision = desiredRev.String
	n.AppliedRevision = appliedRev.String
	n.ReplacementStatus = replacement.String
	n.AgentStatus = agentStatus.String
	n.LegacyMAC = legacyMAC.String
	n.IdentityGeneration = gen
	var err error
	if n.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if n.UpdatedAt, err = parseTimeVal(updated); err != nil {
		return nil, err
	}
	if n.RetiredAt, err = parseTime(retired); err != nil {
		return nil, err
	}
	return &n, nil
}

// CreateDeclarativeNode inserts a declarative node.
func (s *Store) CreateDeclarativeNode(ctx context.Context, n *model.DeclarativeNode) error {
	n.CreatedAt = now()
	n.UpdatedAt = now()
	if n.Lifecycle == "" {
		n.Lifecycle = model.NodeLifecycleDraft
	}
	if n.Status == "" {
		n.Status = model.NodeStatusPending
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO declarative_node
		(id, cluster_id, node_id, role, profile_id, lifecycle, status, labels_json, addresses_json,
		 os_name, os_version, arch, desired_revision, applied_revision, identity_generation,
		 replacement_status, agent_status, legacy_mac, created_at, updated_at, retired_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		n.ID, n.ClusterID, n.NodeID, n.Role, nullString(n.ProfileID), n.Lifecycle, n.Status, n.LabelsJSON,
		n.AddressesJSON, nullString(n.OSName), nullString(n.OSVersion), nullString(n.Arch),
		nullString(n.DesiredRevision), nullString(n.AppliedRevision), n.IdentityGeneration,
		nullString(n.ReplacementStatus), nullString(n.AgentStatus), nullString(n.LegacyMAC),
		ts(n.CreatedAt), ts(n.UpdatedAt), nullTime(n.RetiredAt))
	return err
}

// DeclarativeNodeByID finds a declarative node by primary key.
func (s *Store) DeclarativeNodeByID(ctx context.Context, id string) (*model.DeclarativeNode, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+declarativeNodeColumns+` FROM declarative_node WHERE id=$1`, id)
	n, err := scanDeclarativeNode(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return n, nil
}

// DeclarativeNodeByNodeID finds a declarative node by cluster+node id (stable identity).
func (s *Store) DeclarativeNodeByNodeID(ctx context.Context, clusterID, nodeID string) (*model.DeclarativeNode, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+declarativeNodeColumns+`
		FROM declarative_node WHERE cluster_id=$1 AND node_id=$2`, clusterID, nodeID)
	n, err := scanDeclarativeNode(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return n, nil
}

// ListDeclarativeNodes lists declarative nodes.
func (s *Store) ListDeclarativeNodes(ctx context.Context, clusterID, lifecycle string) ([]*model.DeclarativeNode, error) {
	q := `SELECT ` + declarativeNodeColumns + ` FROM declarative_node`
	conds := []string{}
	args := []any{}
	if clusterID != "" {
		args = append(args, clusterID)
		conds = append(conds, `cluster_id=$`+strconv.Itoa(len(args)))
	}
	if lifecycle != "" {
		args = append(args, lifecycle)
		conds = append(conds, `lifecycle=$`+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY created_at`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.DeclarativeNode{}
	for rows.Next() {
		n, err := scanDeclarativeNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UpdateDeclarativeNode updates mutable node fields.
func (s *Store) UpdateDeclarativeNode(ctx context.Context, n *model.DeclarativeNode) error {
	n.UpdatedAt = now()
	_, err := s.db.ExecContext(ctx, `UPDATE declarative_node SET
		role=$3, profile_id=$4, lifecycle=$5, status=$6, labels_json=$7, addresses_json=$8,
		os_name=$9, os_version=$10, arch=$11, desired_revision=$12, applied_revision=$13,
		identity_generation=$14, replacement_status=$15, agent_status=$16, legacy_mac=$17,
		updated_at=$18, retired_at=$19
		WHERE id=$1 AND cluster_id=$2`,
		n.ID, n.ClusterID, n.Role, nullString(n.ProfileID), n.Lifecycle, n.Status, n.LabelsJSON,
		n.AddressesJSON, nullString(n.OSName), nullString(n.OSVersion), nullString(n.Arch),
		nullString(n.DesiredRevision), nullString(n.AppliedRevision), n.IdentityGeneration,
		nullString(n.ReplacementStatus), nullString(n.AgentStatus), nullString(n.LegacyMAC),
		ts(n.UpdatedAt), nullTime(n.RetiredAt))
	return err
}

const serviceReferenceColumns = `id, cluster_id, name, service_instance_id, node_id, address, port, secret_ref_json, status, created_at, updated_at`

func scanServiceReference(row interface{ Scan(...any) error }) (*model.ServiceReference, error) {
	var r model.ServiceReference
	var secret, svcID, nodeID, address sql.NullString
	var created, updated sql.NullString
	if err := row.Scan(&r.ID, &r.ClusterID, &r.Name, &svcID, &nodeID, &address, &r.Port,
		&secret, &r.Status, &created, &updated); err != nil {
		return nil, err
	}
	r.ServiceInstanceID = svcID.String
	r.NodeID = nodeID.String
	r.Address = address.String
	if secret.Valid && secret.String != "" {
		var ref model.SecretRef
		if err := json.Unmarshal([]byte(secret.String), &ref); err != nil {
			return nil, err
		}
		r.SecretRef = &ref
	}
	var err error
	if r.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if r.UpdatedAt, err = parseTimeVal(updated); err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateServiceReference inserts a service reference.
func (s *Store) CreateServiceReference(ctx context.Context, r *model.ServiceReference) error {
	r.CreatedAt = now()
	r.UpdatedAt = now()
	if r.Status == "" {
		r.Status = "active"
	}
	var secret any
	if r.SecretRef != nil {
		b, err := json.Marshal(r.SecretRef)
		if err != nil {
			return err
		}
		secret = string(b)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO service_reference
		(id, cluster_id, name, service_instance_id, node_id, address, port, secret_ref_json, status, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		r.ID, r.ClusterID, r.Name, nullString(r.ServiceInstanceID), nullString(r.NodeID),
		nullString(r.Address), r.Port, secret, r.Status, ts(r.CreatedAt), ts(r.UpdatedAt))
	return err
}

// ServiceReferenceByID finds a service reference.
func (s *Store) ServiceReferenceByID(ctx context.Context, id string) (*model.ServiceReference, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+serviceReferenceColumns+` FROM service_reference WHERE id=$1`, id)
	r, err := scanServiceReference(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return r, nil
}

// ListServiceReferences lists references for a cluster.
func (s *Store) ListServiceReferences(ctx context.Context, clusterID string) ([]*model.ServiceReference, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+serviceReferenceColumns+`
		FROM service_reference WHERE cluster_id=$1 ORDER BY name`, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.ServiceReference{}
	for rows.Next() {
		r, err := scanServiceReference(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateServiceReference updates mutable fields.
func (s *Store) UpdateServiceReference(ctx context.Context, r *model.ServiceReference) error {
	r.UpdatedAt = now()
	var secret any
	if r.SecretRef != nil {
		b, err := json.Marshal(r.SecretRef)
		if err != nil {
			return err
		}
		secret = string(b)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE service_reference SET
		service_instance_id=$3, node_id=$4, address=$5, port=$6, secret_ref_json=$7, status=$8, updated_at=$9
		WHERE id=$1 AND cluster_id=$2`,
		r.ID, r.ClusterID, nullString(r.ServiceInstanceID), nullString(r.NodeID), nullString(r.Address),
		r.Port, secret, r.Status, ts(r.UpdatedAt))
	return err
}

const operationColumns = `id, operation_id, operation_type, cluster_id, node_id, module_id, service_instance_id,
	desired_revision, arguments_json, approval, risk_level, idempotency_key, deadline, primary_epoch,
	status, requested_by, created_at, started_at, finished_at, error_code, error_message`

func scanOperation(row interface{ Scan(...any) error }) (*model.Operation, error) {
	var o model.Operation
	var deadline, created, started, finished sql.NullString
	var clusterID, nodeID, moduleID, svcID, desiredRev, approval, risk, idem, requestedBy, errCode, errMsg sql.NullString
	var epoch int64
	if err := row.Scan(&o.ID, &o.OperationID, &o.OperationType, &clusterID, &nodeID, &moduleID,
		&svcID, &desiredRev, &o.ArgumentsJSON, &approval, &risk,
		&idem, &deadline, &epoch, &o.Status, &requestedBy, &created, &started, &finished,
		&errCode, &errMsg); err != nil {
		return nil, err
	}
	o.ClusterID = clusterID.String
	o.NodeID = nodeID.String
	o.ModuleID = moduleID.String
	o.ServiceInstanceID = svcID.String
	o.DesiredRevision = desiredRev.String
	o.Approval = approval.String
	o.RiskLevel = risk.String
	o.IdempotencyKey = idem.String
	o.RequestedBy = requestedBy.String
	o.ErrorCode = errCode.String
	o.ErrorMessage = errMsg.String
	o.PrimaryEpoch = epoch
	var err error
	if o.Deadline, err = parseTime(deadline); err != nil {
		return nil, err
	}
	if o.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if o.StartedAt, err = parseTime(started); err != nil {
		return nil, err
	}
	if o.FinishedAt, err = parseTime(finished); err != nil {
		return nil, err
	}
	return &o, nil
}

// CreateOperation inserts a planned operation.
func (s *Store) CreateOperation(ctx context.Context, o *model.Operation) error {
	o.CreatedAt = now()
	if o.Status == "" {
		o.Status = model.OpStatusPlanned
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO operation_v2
		(id, operation_id, operation_type, cluster_id, node_id, module_id, service_instance_id,
		 desired_revision, arguments_json, approval, risk_level, idempotency_key, deadline, primary_epoch,
		 status, requested_by, created_at, started_at, finished_at, error_code, error_message)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		o.ID, o.OperationID, o.OperationType, nullString(o.ClusterID), nullString(o.NodeID),
		nullString(o.ModuleID), nullString(o.ServiceInstanceID), nullString(o.DesiredRevision),
		o.ArgumentsJSON, nullString(o.Approval), nullString(o.RiskLevel), nullString(o.IdempotencyKey),
		nullTime(o.Deadline), o.PrimaryEpoch, o.Status, nullString(o.RequestedBy), ts(o.CreatedAt),
		nullTime(o.StartedAt), nullTime(o.FinishedAt), nullString(o.ErrorCode), nullString(o.ErrorMessage))
	return err
}

// OperationByID finds an operation.
func (s *Store) OperationByID(ctx context.Context, id string) (*model.Operation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operation_v2 WHERE id=$1`, id)
	o, err := scanOperation(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return o, nil
}

// OperationByOperationID finds an operation by its stable operation_id.
func (s *Store) OperationByOperationID(ctx context.Context, operationID string) (*model.Operation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+operationColumns+` FROM operation_v2 WHERE operation_id=$1`, operationID)
	o, err := scanOperation(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return o, nil
}

// OperationByIdempotency finds an operation by requested_by + idempotency key.
func (s *Store) OperationByIdempotency(ctx context.Context, requestedBy, key string) (*model.Operation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+operationColumns+`
		FROM operation_v2 WHERE requested_by=$1 AND idempotency_key=$2`, requestedBy, key)
	o, err := scanOperation(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return o, nil
}

// ListOperations lists operations with optional filters.
func (s *Store) ListOperations(ctx context.Context, clusterID, nodeID, status string, limit, offset int) ([]*model.Operation, error) {
	q := `SELECT ` + operationColumns + ` FROM operation_v2`
	conds := []string{}
	args := []any{}
	if clusterID != "" {
		args = append(args, clusterID)
		conds = append(conds, `cluster_id=$`+strconv.Itoa(len(args)))
	}
	if nodeID != "" {
		args = append(args, nodeID)
		conds = append(conds, `node_id=$`+strconv.Itoa(len(args)))
	}
	if status != "" {
		args = append(args, status)
		conds = append(conds, `status=$`+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY created_at DESC`
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
	out := []*model.Operation{}
	for rows.Next() {
		o, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// UpdateOperationStatus transitions an operation status with an optional
// error. Only non-terminal -> terminal transitions are persisted once.
func (s *Store) UpdateOperationStatus(ctx context.Context, id, status, errorCode, errorMessage string) error {
	nowTs := now()
	var finished any
	if model.IsOperationTerminal(status) {
		finished = ts(nowTs)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE operation_v2 SET
		status=$2, error_code=$3, error_message=$4, finished_at=COALESCE(finished_at, $5)
		WHERE id=$1`,
		id, status, nullString(errorCode), nullString(errorMessage), finished)
	return err
}

const operationStepColumns = `id, operation_id, sequence, module_id, operation, attempt, commit_point, status, error_type, message, started_at, completed_at`

func scanOperationStep(row interface{ Scan(...any) error }) (*model.OperationStep, error) {
	var st model.OperationStep
	var started, completed sql.NullString
	var moduleID, operation, commitPoint, errorType, message sql.NullString
	if err := row.Scan(&st.ID, &st.OperationID, &st.Sequence, &moduleID, &operation, &st.Attempt,
		&commitPoint, &st.Status, &errorType, &message, &started, &completed); err != nil {
		return nil, err
	}
	st.ModuleID = moduleID.String
	st.Operation = operation.String
	st.CommitPoint = commitPoint.String
	st.ErrorType = errorType.String
	st.Message = message.String
	var err error
	if st.StartedAt, err = parseTime(started); err != nil {
		return nil, err
	}
	if st.CompletedAt, err = parseTime(completed); err != nil {
		return nil, err
	}
	return &st, nil
}

// CreateOperationStep inserts a step.
func (s *Store) CreateOperationStep(ctx context.Context, st *model.OperationStep) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO operation_step
		(id, operation_id, sequence, module_id, operation, attempt, commit_point, status, error_type, message, started_at, completed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		st.ID, st.OperationID, st.Sequence, nullString(st.ModuleID), nullString(st.Operation), st.Attempt,
		nullString(st.CommitPoint), st.Status, nullString(st.ErrorType), nullString(st.Message),
		nullTime(st.StartedAt), nullTime(st.CompletedAt))
	return err
}

// ListOperationSteps lists steps for an operation.
func (s *Store) ListOperationSteps(ctx context.Context, operationID string) ([]*model.OperationStep, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+operationStepColumns+`
		FROM operation_step WHERE operation_id=$1 ORDER BY sequence`, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.OperationStep{}
	for rows.Next() {
		st, err := scanOperationStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// UpdateOperationStepStatus updates a step and its commit point.
func (s *Store) UpdateOperationStepStatus(ctx context.Context, id, status, commitPoint, errorType, message string) error {
	nowTs := now()
	var completed any
	if status == "succeeded" || status == "failed" || status == "blocked" {
		completed = ts(nowTs)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE operation_step SET
		status=$2, commit_point=$3, error_type=$4, message=$5,
		completed_at=COALESCE(completed_at, $6)
		WHERE id=$1`,
		id, status, nullString(commitPoint), nullString(errorType), nullString(message), completed)
	return err
}

const backupSetColumns = `id, backup_id, recovery_set_id, cluster_id, node_id, service_instance_id,
	module_version, app_version, schema_version, files_json, sha256, size_bytes, oss_key, status, created_at`

func scanBackupSet(row interface{ Scan(...any) error }) (*model.BackupSet, error) {
	var b model.BackupSet
	var created, recoverySetID, clusterID, nodeID, svcID, modVer, appVer, schemaVer, files, sha, ossKey sql.NullString
	if err := row.Scan(&b.ID, &b.BackupID, &recoverySetID, &clusterID, &nodeID, &svcID,
		&modVer, &appVer, &schemaVer, &files, &sha, &b.SizeBytes,
		&ossKey, &b.Status, &created); err != nil {
		return nil, err
	}
	b.RecoverySetID = recoverySetID.String
	b.ClusterID = clusterID.String
	b.NodeID = nodeID.String
	b.ServiceInstanceID = svcID.String
	b.ModuleVersion = modVer.String
	b.AppVersion = appVer.String
	b.SchemaVersion = schemaVer.String
	b.FilesJSON = files.String
	b.SHA256 = sha.String
	b.OSSKey = ossKey.String
	var err error
	if b.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	return &b, nil
}

// CreateBackupSet records a backup.
func (s *Store) CreateBackupSet(ctx context.Context, b *model.BackupSet) error {
	b.CreatedAt = now()
	if b.Status == "" {
		b.Status = "verified"
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO backup_set
		(id, backup_id, recovery_set_id, cluster_id, node_id, service_instance_id,
		 module_version, app_version, schema_version, files_json, sha256, size_bytes, oss_key, status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		b.ID, b.BackupID, nullString(b.RecoverySetID), nullString(b.ClusterID), nullString(b.NodeID),
		nullString(b.ServiceInstanceID), nullString(b.ModuleVersion), nullString(b.AppVersion),
		nullString(b.SchemaVersion), b.FilesJSON, nullString(b.SHA256), b.SizeBytes, nullString(b.OSSKey),
		b.Status, ts(b.CreatedAt))
	return err
}

// BackupSetByID finds a backup set.
func (s *Store) BackupSetByID(ctx context.Context, id string) (*model.BackupSet, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+backupSetColumns+` FROM backup_set WHERE id=$1`, id)
	b, err := scanBackupSet(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return b, nil
}

// ListBackupSets lists backups.
func (s *Store) ListBackupSets(ctx context.Context, nodeID, status string, limit int) ([]*model.BackupSet, error) {
	q := `SELECT ` + backupSetColumns + ` FROM backup_set`
	conds := []string{}
	args := []any{}
	if nodeID != "" {
		args = append(args, nodeID)
		conds = append(conds, `node_id=$`+strconv.Itoa(len(args)))
	}
	if status != "" {
		args = append(args, status)
		conds = append(conds, `status=$`+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY created_at DESC`
	if limit > 0 {
		args = append(args, limit)
		q += ` LIMIT $` + strconv.Itoa(len(args))
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.BackupSet{}
	for rows.Next() {
		b, err := scanBackupSet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

const releaseCacheColumns = `id, version, source_repository, source_release, os, arch, artifact_name,
	artifact_size, sha256, modules_version, schema_min, schema_max, oss_key, status, uploaded_at, verified_at, created_at`

func scanReleaseCache(row interface{ Scan(...any) error }) (*model.ReleaseCacheEntry, error) {
	var e model.ReleaseCacheEntry
	var uploaded, verified, created sql.NullString
	var repo, release, osName, arch, modVer, schemaMin, schemaMax, ossKey sql.NullString
	if err := row.Scan(&e.ID, &e.Version, &repo, &release, &osName, &arch,
		&e.ArtifactName, &e.ArtifactSize, &e.SHA256, &modVer, &schemaMin, &schemaMax,
		&ossKey, &e.Status, &uploaded, &verified, &created); err != nil {
		return nil, err
	}
	e.SourceRepository = repo.String
	e.SourceRelease = release.String
	e.OS = osName.String
	e.Arch = arch.String
	e.ModulesVersion = modVer.String
	e.SchemaMin = schemaMin.String
	e.SchemaMax = schemaMax.String
	e.OSSKey = ossKey.String
	var err error
	if e.UploadedAt, err = parseTime(uploaded); err != nil {
		return nil, err
	}
	if e.VerifiedAt, err = parseTime(verified); err != nil {
		return nil, err
	}
	if e.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	return &e, nil
}

// CreateReleaseCacheEntry records a release cache entry.
func (s *Store) CreateReleaseCacheEntry(ctx context.Context, e *model.ReleaseCacheEntry) error {
	e.CreatedAt = now()
	if e.Status == "" {
		e.Status = model.ReleaseCachePending
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO release_cache_entry
		(id, version, source_repository, source_release, os, arch, artifact_name, artifact_size,
		 sha256, modules_version, schema_min, schema_max, oss_key, status, uploaded_at, verified_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		e.ID, e.Version, nullString(e.SourceRepository), nullString(e.SourceRelease), nullString(e.OS),
		nullString(e.Arch), e.ArtifactName, e.ArtifactSize, e.SHA256, nullString(e.ModulesVersion),
		nullString(e.SchemaMin), nullString(e.SchemaMax), nullString(e.OSSKey), e.Status,
		nullTime(e.UploadedAt), nullTime(e.VerifiedAt), ts(e.CreatedAt))
	return err
}

// ReleaseCacheEntryByID finds a release cache entry.
func (s *Store) ReleaseCacheEntryByID(ctx context.Context, id string) (*model.ReleaseCacheEntry, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+releaseCacheColumns+` FROM release_cache_entry WHERE id=$1`, id)
	e, err := scanReleaseCache(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return e, nil
}

// ReleaseCacheEntryByVersion finds a release cache entry by version + artifact.
func (s *Store) ReleaseCacheEntryByVersion(ctx context.Context, version, artifact string) (*model.ReleaseCacheEntry, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+releaseCacheColumns+`
		FROM release_cache_entry WHERE version=$1 AND artifact_name=$2`, version, artifact)
	e, err := scanReleaseCache(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return e, nil
}

// ListReleaseCacheEntries lists release cache entries.
func (s *Store) ListReleaseCacheEntries(ctx context.Context, version, status string) ([]*model.ReleaseCacheEntry, error) {
	q := `SELECT ` + releaseCacheColumns + ` FROM release_cache_entry`
	conds := []string{}
	args := []any{}
	if version != "" {
		args = append(args, version)
		conds = append(conds, `version=$`+strconv.Itoa(len(args)))
	}
	if status != "" {
		args = append(args, status)
		conds = append(conds, `status=$`+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.ReleaseCacheEntry{}
	for rows.Next() {
		e, err := scanReleaseCache(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateReleaseCacheEntryStatus updates status + verified/uploaded timestamps.
func (s *Store) UpdateReleaseCacheEntryStatus(ctx context.Context, id, status string, uploadedAt, verifiedAt *time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE release_cache_entry SET
		status=$2, uploaded_at=$3, verified_at=$4 WHERE id=$1`,
		id, status, nullTime(uploadedAt), nullTime(verifiedAt))
	return err
}

const ossSyncColumns = `id, cluster_id, kind, object_key, sha256, direction, status, etag, created_at, verified_at`

func scanOSSSync(row interface{ Scan(...any) error }) (*model.OSSSyncRevision, error) {
	var o model.OSSSyncRevision
	var created, verified, clusterID, sha, etag sql.NullString
	if err := row.Scan(&o.ID, &clusterID, &o.Kind, &o.ObjectKey, &sha, &o.Direction, &o.Status,
		&etag, &created, &verified); err != nil {
		return nil, err
	}
	o.ClusterID = clusterID.String
	o.SHA256 = sha.String
	o.Etag = etag.String
	var err error
	if o.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if o.VerifiedAt, err = parseTime(verified); err != nil {
		return nil, err
	}
	return &o, nil
}

// CreateOSSSyncRevision records a sync attempt.
func (s *Store) CreateOSSSyncRevision(ctx context.Context, o *model.OSSSyncRevision) error {
	o.CreatedAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO oss_sync_revision
		(id, cluster_id, kind, object_key, sha256, direction, status, etag, created_at, verified_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		o.ID, nullString(o.ClusterID), o.Kind, o.ObjectKey, nullString(o.SHA256), o.Direction, o.Status,
		nullString(o.Etag), ts(o.CreatedAt), nullTime(o.VerifiedAt))
	return err
}

// ListOSSSyncRevisions lists sync records.
func (s *Store) ListOSSSyncRevisions(ctx context.Context, kind string, limit int) ([]*model.OSSSyncRevision, error) {
	q := `SELECT ` + ossSyncColumns + ` FROM oss_sync_revision`
	args := []any{}
	if kind != "" {
		q += ` WHERE kind=$1`
		args = append(args, kind)
	}
	q += ` ORDER BY created_at DESC`
	if limit > 0 {
		args = append(args, limit)
		q += ` LIMIT $` + strconv.Itoa(len(args))
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.OSSSyncRevision{}
	for rows.Next() {
		o, err := scanOSSSync(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

const primaryTransferColumns = `id, cluster_id, from_node_id, to_node_id, primary_epoch, status, backup_set_id,
	steps_json, error_code, error_message, requested_by, created_at, started_at, completed_at`

func scanPrimaryTransfer(row interface{ Scan(...any) error }) (*model.PrimaryTransfer, error) {
	var t model.PrimaryTransfer
	var created, started, completed, errCode, errMsg, backupID, requestedBy sql.NullString
	if err := row.Scan(&t.ID, &t.ClusterID, &t.FromNodeID, &t.ToNodeID, &t.PrimaryEpoch, &t.Status,
		&backupID, &t.StepsJSON, &errCode, &errMsg, &requestedBy, &created, &started, &completed); err != nil {
		return nil, err
	}
	t.BackupSetID = backupID.String
	t.ErrorCode = errCode.String
	t.ErrorMessage = errMsg.String
	t.RequestedBy = requestedBy.String
	var err error
	if t.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if t.StartedAt, err = parseTime(started); err != nil {
		return nil, err
	}
	if t.CompletedAt, err = parseTime(completed); err != nil {
		return nil, err
	}
	return &t, nil
}

// CreatePrimaryTransfer inserts a transfer plan.
func (s *Store) CreatePrimaryTransfer(ctx context.Context, t *model.PrimaryTransfer) error {
	t.CreatedAt = now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO primary_transfer
		(id, cluster_id, from_node_id, to_node_id, primary_epoch, status, backup_set_id,
		 steps_json, error_code, error_message, requested_by, created_at, started_at, completed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		t.ID, t.ClusterID, t.FromNodeID, t.ToNodeID, t.PrimaryEpoch, t.Status, nullString(t.BackupSetID),
		t.StepsJSON, nullString(t.ErrorCode), nullString(t.ErrorMessage), nullString(t.RequestedBy),
		ts(t.CreatedAt), nullTime(t.StartedAt), nullTime(t.CompletedAt))
	return err
}

// PrimaryTransferByID finds a transfer.
func (s *Store) PrimaryTransferByID(ctx context.Context, id string) (*model.PrimaryTransfer, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+primaryTransferColumns+` FROM primary_transfer WHERE id=$1`, id)
	t, err := scanPrimaryTransfer(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return t, nil
}

// ListPrimaryTransfers lists transfers.
func (s *Store) ListPrimaryTransfers(ctx context.Context, clusterID string) ([]*model.PrimaryTransfer, error) {
	args := []any{clusterID}
	rows, err := s.db.QueryContext(ctx, `SELECT `+primaryTransferColumns+`
		FROM primary_transfer WHERE cluster_id=$1 ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.PrimaryTransfer{}
	for rows.Next() {
		t, err := scanPrimaryTransfer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdatePrimaryTransferStatus updates transfer status + error.
func (s *Store) UpdatePrimaryTransferStatus(ctx context.Context, id, status, errorCode, errorMessage string) error {
	nowTs := now()
	var completed any
	if status == model.TransferCompleted || status == model.TransferFailed || status == model.TransferRollbackRequired {
		completed = ts(nowTs)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE primary_transfer SET
		status=$2, error_code=$3, error_message=$4, completed_at=COALESCE(completed_at, $5)
		WHERE id=$1`,
		id, status, nullString(errorCode), nullString(errorMessage), completed)
	return err
}

// helpers
func rowsAffectedErr(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
