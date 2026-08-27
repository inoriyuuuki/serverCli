// Deployment store accessors for the deployment management module. Tables are
// created by migration 0007 (deployment_feature, deployment_release,
// oss_profile, deployment_config_profile, deployment_secret_reference,
// deployment_target, deployment_target_secret, deployment_operation,
// deployment_operation_target, deployment_step, deployment_backup,
// bootstrap_session); migration 0008 adds the per-node serial constraint on
// active operation targets.
package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"servercli/internal/model"
)

// conflict maps database uniqueness violations to ErrConflict. SQLite and
// PostgreSQL use different message formats, so match on the shared fragment.
func conflict(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unique constraint") || strings.Contains(msg, "duplicate key") {
		return ErrConflict
	}
	return err
}

// ---- deployment_feature ----

const deploymentFeatureColumns = `id, feature_key, name, description, os, arch, config_schema_json,
	backup_mode, rollback_capability, dependencies_json, minimum_agent_version, default_version, created_at, updated_at`

func scanDeploymentFeature(row interface{ Scan(...any) error }) (*model.DeploymentFeature, error) {
	var f model.DeploymentFeature
	var cfgSchema, deps, defaultVer, created, updated sql.NullString
	if err := row.Scan(&f.ID, &f.FeatureKey, &f.Name, &f.Description, &f.OS, &f.Arch, &cfgSchema,
		&f.BackupMode, &f.RollbackCapability, &deps, &f.MinimumAgentVersion, &defaultVer, &created, &updated); err != nil {
		return nil, err
	}
	f.ConfigSchemaJSON = cfgSchema.String
	f.DependenciesJSON = deps.String
	f.DefaultVersion = defaultVer.String
	var err error
	if f.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if f.UpdatedAt, err = parseTimeVal(updated); err != nil {
		return nil, err
	}
	return &f, nil
}

// CreateDeploymentFeature inserts a feature; a duplicate feature_key returns
// ErrConflict.
func (s *Store) CreateDeploymentFeature(ctx context.Context, f *model.DeploymentFeature) error {
	if f.ID == "" {
		f.ID = model.NewUUID()
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = now()
	}
	if f.UpdatedAt.IsZero() {
		f.UpdatedAt = f.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployment_feature
		(id, feature_key, name, description, os, arch, config_schema_json,
		 backup_mode, rollback_capability, dependencies_json, minimum_agent_version, default_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		f.ID, f.FeatureKey, f.Name, f.Description, f.OS, f.Arch, nullString(f.ConfigSchemaJSON),
		f.BackupMode, f.RollbackCapability, nullString(f.DependenciesJSON), f.MinimumAgentVersion,
		nullString(f.DefaultVersion), ts(f.CreatedAt), ts(f.UpdatedAt))
	return conflict(err)
}

// DeploymentFeatureByID finds a feature by id.
func (s *Store) DeploymentFeatureByID(ctx context.Context, id string) (*model.DeploymentFeature, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deploymentFeatureColumns+` FROM deployment_feature WHERE id=$1`, id)
	f, err := scanDeploymentFeature(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return f, nil
}

// DeploymentFeatureByKey finds a feature by its unique feature_key.
func (s *Store) DeploymentFeatureByKey(ctx context.Context, key string) (*model.DeploymentFeature, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deploymentFeatureColumns+` FROM deployment_feature WHERE feature_key=$1`, key)
	f, err := scanDeploymentFeature(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return f, nil
}

// ListDeploymentFeatures returns all features.
func (s *Store) ListDeploymentFeatures(ctx context.Context) ([]*model.DeploymentFeature, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentFeatureColumns+` FROM deployment_feature ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.DeploymentFeature{}
	for rows.Next() {
		f, err := scanDeploymentFeature(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpdateDeploymentFeature persists editable feature fields.
func (s *Store) UpdateDeploymentFeature(ctx context.Context, f *model.DeploymentFeature) error {
	if f.UpdatedAt.IsZero() {
		f.UpdatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE deployment_feature SET
		feature_key=$1, name=$2, description=$3, os=$4, arch=$5, config_schema_json=$6,
		backup_mode=$7, rollback_capability=$8, dependencies_json=$9, minimum_agent_version=$10,
		default_version=$11, updated_at=$12
		WHERE id=$13`,
		f.FeatureKey, f.Name, f.Description, f.OS, f.Arch, nullString(f.ConfigSchemaJSON),
		f.BackupMode, f.RollbackCapability, nullString(f.DependenciesJSON), f.MinimumAgentVersion,
		nullString(f.DefaultVersion), ts(f.UpdatedAt), f.ID)
	return conflict(err)
}

// DeleteDeploymentFeature removes a feature. When targets still reference it
// the FK RESTRICT constraint rejects the delete and the database error is
// returned.
func (s *Store) DeleteDeploymentFeature(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deployment_feature WHERE id=$1`, id)
	return err
}

// ---- deployment_release ----

const deploymentReleaseColumns = `id, feature_id, version, source_commit, object_key, size, sha256, signature,
	install_hook, update_hook, backup_hook, health_hook, rollback_hook, backup_mode,
	data_migration_metadata_json, manifest_hash, created_at`

func scanDeploymentRelease(row interface{ Scan(...any) error }) (*model.DeploymentRelease, error) {
	var r model.DeploymentRelease
	var srcCommit, signature, dataMig, manifestHash, created sql.NullString
	if err := row.Scan(&r.ID, &r.FeatureID, &r.Version, &srcCommit, &r.ObjectKey, &r.Size, &r.SHA256, &signature,
		&r.InstallHook, &r.UpdateHook, &r.BackupHook, &r.HealthHook, &r.RollbackHook, &r.BackupMode,
		&dataMig, &manifestHash, &created); err != nil {
		return nil, err
	}
	r.SourceCommit = srcCommit.String
	r.Signature = signature.String
	r.DataMigrationMetadataJSON = dataMig.String
	r.ManifestHash = manifestHash.String
	var err error
	if r.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateDeploymentRelease inserts an immutable release; a duplicate
// (feature_id, version) returns ErrConflict.
func (s *Store) CreateDeploymentRelease(ctx context.Context, r *model.DeploymentRelease) error {
	if r.ID == "" {
		r.ID = model.NewUUID()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployment_release
		(id, feature_id, version, source_commit, object_key, size, sha256, signature,
		 install_hook, update_hook, backup_hook, health_hook, rollback_hook, backup_mode,
		 data_migration_metadata_json, manifest_hash, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		r.ID, r.FeatureID, r.Version, nullString(r.SourceCommit), r.ObjectKey, r.Size, r.SHA256, nullString(r.Signature),
		r.InstallHook, r.UpdateHook, r.BackupHook, r.HealthHook, r.RollbackHook, r.BackupMode,
		nullString(r.DataMigrationMetadataJSON), nullString(r.ManifestHash), ts(r.CreatedAt))
	return conflict(err)
}

// DeploymentReleaseByID finds a release.
func (s *Store) DeploymentReleaseByID(ctx context.Context, id string) (*model.DeploymentRelease, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deploymentReleaseColumns+` FROM deployment_release WHERE id=$1`, id)
	r, err := scanDeploymentRelease(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return r, nil
}

// ListDeploymentReleases returns releases, optionally filtered by feature
// (empty featureID = all), newest first.
func (s *Store) ListDeploymentReleases(ctx context.Context, featureID string) ([]*model.DeploymentRelease, error) {
	q := `SELECT ` + deploymentReleaseColumns + ` FROM deployment_release`
	args := []any{}
	if featureID != "" {
		q += ` WHERE feature_id=$1`
		args = append(args, featureID)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.DeploymentRelease{}
	for rows.Next() {
		r, err := scanDeploymentRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteDeploymentRelease removes a release.
func (s *Store) DeleteDeploymentRelease(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deployment_release WHERE id=$1`, id)
	return err
}

// ---- oss_profile ----

const ossProfileColumns = `id, name, endpoint, region, bucket, prefix, access_key_id_enc, access_key_secret_enc,
	is_private, last_tested_at, last_test_result, created_at, updated_at`

func scanOSSProfile(row interface{ Scan(...any) error }) (*model.OSSProfile, error) {
	var p model.OSSProfile
	var lastTested, lastResult, created, updated sql.NullString
	var isPrivate int64
	if err := row.Scan(&p.ID, &p.Name, &p.Endpoint, &p.Region, &p.Bucket, &p.Prefix,
		&p.AccessKeyIDEnc, &p.AccessKeySecretEnc, &isPrivate, &lastTested, &lastResult, &created, &updated); err != nil {
		return nil, err
	}
	p.IsPrivate = parseBool(isPrivate)
	p.LastTestResult = lastResult.String
	var err error
	if p.LastTestedAt, err = parseTime(lastTested); err != nil {
		return nil, err
	}
	if p.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if p.UpdatedAt, err = parseTimeVal(updated); err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateOSSProfile inserts an OSS profile; a duplicate name returns
// ErrConflict. Access key material must already be encrypted by the caller.
func (s *Store) CreateOSSProfile(ctx context.Context, p *model.OSSProfile) error {
	if p.ID == "" {
		p.ID = model.NewUUID()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now()
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = p.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO oss_profile
		(id, name, endpoint, region, bucket, prefix, access_key_id_enc, access_key_secret_enc,
		 is_private, last_tested_at, last_test_result, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		p.ID, p.Name, p.Endpoint, p.Region, p.Bucket, p.Prefix,
		p.AccessKeyIDEnc, p.AccessKeySecretEnc, boolInt(p.IsPrivate),
		nullTime(p.LastTestedAt), nullString(p.LastTestResult), ts(p.CreatedAt), ts(p.UpdatedAt))
	return conflict(err)
}

// OSSProfileByID finds an OSS profile.
func (s *Store) OSSProfileByID(ctx context.Context, id string) (*model.OSSProfile, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+ossProfileColumns+` FROM oss_profile WHERE id=$1`, id)
	p, err := scanOSSProfile(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return p, nil
}

// ListOSSProfiles returns all OSS profiles.
func (s *Store) ListOSSProfiles(ctx context.Context) ([]*model.OSSProfile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ossProfileColumns+` FROM oss_profile ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.OSSProfile{}
	for rows.Next() {
		p, err := scanOSSProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateOSSProfile persists editable OSS profile fields.
func (s *Store) UpdateOSSProfile(ctx context.Context, p *model.OSSProfile) error {
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE oss_profile SET
		name=$1, endpoint=$2, region=$3, bucket=$4, prefix=$5, access_key_id_enc=$6,
		access_key_secret_enc=$7, is_private=$8, last_tested_at=$9, last_test_result=$10, updated_at=$11
		WHERE id=$12`,
		p.Name, p.Endpoint, p.Region, p.Bucket, p.Prefix, p.AccessKeyIDEnc,
		p.AccessKeySecretEnc, boolInt(p.IsPrivate), nullTime(p.LastTestedAt), nullString(p.LastTestResult),
		ts(p.UpdatedAt), p.ID)
	return conflict(err)
}

// DeleteOSSProfile removes an OSS profile.
func (s *Store) DeleteOSSProfile(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM oss_profile WHERE id=$1`, id)
	return err
}

// ---- deployment_config_profile ----

const deploymentConfigProfileColumns = `id, name, scope_type, scope_id, feature_id, content_json, content_hash,
	version, created_at, updated_at`

func scanDeploymentConfigProfile(row interface{ Scan(...any) error }) (*model.DeploymentConfigProfile, error) {
	var p model.DeploymentConfigProfile
	var created, updated sql.NullString
	if err := row.Scan(&p.ID, &p.Name, &p.ScopeType, &p.ScopeID, &p.FeatureID, &p.ContentJSON, &p.ContentHash,
		&p.Version, &created, &updated); err != nil {
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

// CreateDeploymentConfigProfile inserts a config profile; a duplicate
// (feature_id, scope_type, scope_id, name) returns ErrConflict.
func (s *Store) CreateDeploymentConfigProfile(ctx context.Context, p *model.DeploymentConfigProfile) error {
	if p.ID == "" {
		p.ID = model.NewUUID()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now()
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = p.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployment_config_profile
		(id, name, scope_type, scope_id, feature_id, content_json, content_hash, version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		p.ID, p.Name, p.ScopeType, p.ScopeID, p.FeatureID, p.ContentJSON, p.ContentHash, p.Version,
		ts(p.CreatedAt), ts(p.UpdatedAt))
	return conflict(err)
}

// DeploymentConfigProfileByID finds a config profile.
func (s *Store) DeploymentConfigProfileByID(ctx context.Context, id string) (*model.DeploymentConfigProfile, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deploymentConfigProfileColumns+` FROM deployment_config_profile WHERE id=$1`, id)
	p, err := scanDeploymentConfigProfile(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return p, nil
}

// ListDeploymentConfigProfiles returns config profiles; empty filter values
// mean "no filter".
func (s *Store) ListDeploymentConfigProfiles(ctx context.Context, scopeType, scopeID, featureID string) ([]*model.DeploymentConfigProfile, error) {
	q := `SELECT ` + deploymentConfigProfileColumns + ` FROM deployment_config_profile`
	conds := []string{}
	args := []any{}
	if scopeType != "" {
		args = append(args, scopeType)
		conds = append(conds, `scope_type=$`+strconv.Itoa(len(args)))
	}
	if scopeID != "" {
		args = append(args, scopeID)
		conds = append(conds, `scope_id=$`+strconv.Itoa(len(args)))
	}
	if featureID != "" {
		args = append(args, featureID)
		conds = append(conds, `feature_id=$`+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.DeploymentConfigProfile{}
	for rows.Next() {
		p, err := scanDeploymentConfigProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateDeploymentConfigProfile persists editable config profile fields.
func (s *Store) UpdateDeploymentConfigProfile(ctx context.Context, p *model.DeploymentConfigProfile) error {
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE deployment_config_profile SET
		name=$1, scope_type=$2, scope_id=$3, content_json=$4, content_hash=$5, version=$6, updated_at=$7
		WHERE id=$8`,
		p.Name, p.ScopeType, p.ScopeID, p.ContentJSON, p.ContentHash, p.Version, ts(p.UpdatedAt), p.ID)
	return conflict(err)
}

// DeleteDeploymentConfigProfile removes a config profile.
func (s *Store) DeleteDeploymentConfigProfile(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deployment_config_profile WHERE id=$1`, id)
	return err
}

// ---- deployment_secret_reference ----
// Only the reference metadata is persisted: object_key/version/content_hash/
// encryption_mode/size. There is no content column and the secret body is
// never written.

const deploymentSecretReferenceColumns = `id, name, feature_id, scope_type, scope_id, object_key, version,
	content_hash, encryption_mode, size, updated_at`

func scanDeploymentSecretReference(row interface{ Scan(...any) error }) (*model.DeploymentSecretReference, error) {
	var r model.DeploymentSecretReference
	var updated sql.NullString
	if err := row.Scan(&r.ID, &r.Name, &r.FeatureID, &r.ScopeType, &r.ScopeID, &r.ObjectKey, &r.Version,
		&r.ContentHash, &r.EncryptionMode, &r.Size, &updated); err != nil {
		return nil, err
	}
	var err error
	if r.UpdatedAt, err = parseTimeVal(updated); err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateDeploymentSecretReference inserts a secret reference; a duplicate
// (feature_id, scope_type, scope_id, name) returns ErrConflict.
func (s *Store) CreateDeploymentSecretReference(ctx context.Context, r *model.DeploymentSecretReference) error {
	if r.ID == "" {
		r.ID = model.NewUUID()
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployment_secret_reference
		(id, name, feature_id, scope_type, scope_id, object_key, version, content_hash, encryption_mode, size, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		r.ID, r.Name, r.FeatureID, r.ScopeType, r.ScopeID, r.ObjectKey, r.Version,
		r.ContentHash, r.EncryptionMode, r.Size, ts(r.UpdatedAt))
	return conflict(err)
}

// DeploymentSecretReferenceByID finds a secret reference.
func (s *Store) DeploymentSecretReferenceByID(ctx context.Context, id string) (*model.DeploymentSecretReference, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deploymentSecretReferenceColumns+` FROM deployment_secret_reference WHERE id=$1`, id)
	r, err := scanDeploymentSecretReference(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return r, nil
}

// ListDeploymentSecretReferences returns secret references; empty filter
// values mean "no filter".
func (s *Store) ListDeploymentSecretReferences(ctx context.Context, featureID, scopeType, scopeID string) ([]*model.DeploymentSecretReference, error) {
	q := `SELECT ` + deploymentSecretReferenceColumns + ` FROM deployment_secret_reference`
	conds := []string{}
	args := []any{}
	if featureID != "" {
		args = append(args, featureID)
		conds = append(conds, `feature_id=$`+strconv.Itoa(len(args)))
	}
	if scopeType != "" {
		args = append(args, scopeType)
		conds = append(conds, `scope_type=$`+strconv.Itoa(len(args)))
	}
	if scopeID != "" {
		args = append(args, scopeID)
		conds = append(conds, `scope_id=$`+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	q += ` ORDER BY updated_at, id`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.DeploymentSecretReference{}
	for rows.Next() {
		r, err := scanDeploymentSecretReference(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateDeploymentSecretReference updates the reference metadata
// (object_key/version/content_hash/encryption_mode/size/updated_at). The
// secret body is never touched.
func (s *Store) UpdateDeploymentSecretReference(ctx context.Context, r *model.DeploymentSecretReference) error {
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE deployment_secret_reference SET
		object_key=$1, version=$2, content_hash=$3, encryption_mode=$4, size=$5, updated_at=$6
		WHERE id=$7`,
		r.ObjectKey, r.Version, r.ContentHash, r.EncryptionMode, r.Size, ts(r.UpdatedAt), r.ID)
	return err
}

// DeleteDeploymentSecretReference removes a secret reference.
func (s *Store) DeleteDeploymentSecretReference(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deployment_secret_reference WHERE id=$1`, id)
	return err
}

// ---- deployment_target ----

const deploymentTargetColumns = `id, feature_id, node_id, config_profile_id, override_reference_json,
	desired_release_id, current_release_id, last_healthy_release_id, actual_status, last_health_check_at,
	config_revision, enabled, created_at, updated_at`

func scanDeploymentTarget(row interface{ Scan(...any) error }) (*model.DeploymentTarget, error) {
	var t model.DeploymentTarget
	var cfgProfile, overrideRef, desiredRel, currentRel, lastHealthyRel, lastHealth, created, updated sql.NullString
	var enabled int64
	if err := row.Scan(&t.ID, &t.FeatureID, &t.NodeID, &cfgProfile, &overrideRef,
		&desiredRel, &currentRel, &lastHealthyRel, &t.ActualStatus, &lastHealth,
		&t.ConfigRevision, &enabled, &created, &updated); err != nil {
		return nil, err
	}
	t.ConfigProfileID = cfgProfile.String
	t.OverrideReferenceJSON = overrideRef.String
	t.DesiredReleaseID = desiredRel.String
	t.CurrentReleaseID = currentRel.String
	t.LastHealthyReleaseID = lastHealthyRel.String
	t.Enabled = parseBool(enabled)
	var err error
	if t.LastHealthCheckAt, err = parseTime(lastHealth); err != nil {
		return nil, err
	}
	if t.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if t.UpdatedAt, err = parseTimeVal(updated); err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateDeploymentTarget pins a feature to a node; a duplicate
// (feature_id, node_id) returns ErrConflict.
func (s *Store) CreateDeploymentTarget(ctx context.Context, t *model.DeploymentTarget) error {
	if t.ID == "" {
		t.ID = model.NewUUID()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now()
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = t.CreatedAt
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployment_target
		(id, feature_id, node_id, config_profile_id, override_reference_json,
		 desired_release_id, current_release_id, last_healthy_release_id, actual_status, last_health_check_at,
		 config_revision, enabled, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		t.ID, t.FeatureID, t.NodeID, nullString(t.ConfigProfileID), nullString(t.OverrideReferenceJSON),
		nullString(t.DesiredReleaseID), nullString(t.CurrentReleaseID), nullString(t.LastHealthyReleaseID),
		t.ActualStatus, nullTime(t.LastHealthCheckAt),
		t.ConfigRevision, boolInt(t.Enabled), ts(t.CreatedAt), ts(t.UpdatedAt))
	return conflict(err)
}

// DeploymentTargetByID finds a target.
func (s *Store) DeploymentTargetByID(ctx context.Context, id string) (*model.DeploymentTarget, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deploymentTargetColumns+` FROM deployment_target WHERE id=$1`, id)
	t, err := scanDeploymentTarget(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return t, nil
}

// ListDeploymentTargets returns all targets.
func (s *Store) ListDeploymentTargets(ctx context.Context) ([]*model.DeploymentTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentTargetColumns+` FROM deployment_target ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.DeploymentTarget{}
	for rows.Next() {
		t, err := scanDeploymentTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeploymentTargetsByFeature returns the targets pinned to a feature.
func (s *Store) DeploymentTargetsByFeature(ctx context.Context, featureID string) ([]*model.DeploymentTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentTargetColumns+` FROM deployment_target WHERE feature_id=$1 ORDER BY created_at, id`, featureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.DeploymentTarget{}
	for rows.Next() {
		t, err := scanDeploymentTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeploymentTargetsByNode returns the targets pinned to a node.
func (s *Store) DeploymentTargetsByNode(ctx context.Context, nodeID string) ([]*model.DeploymentTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentTargetColumns+` FROM deployment_target WHERE node_id=$1 ORDER BY created_at, id`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.DeploymentTarget{}
	for rows.Next() {
		t, err := scanDeploymentTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateDeploymentTarget persists editable target fields.
func (s *Store) UpdateDeploymentTarget(ctx context.Context, t *model.DeploymentTarget) error {
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE deployment_target SET
		config_profile_id=$1, override_reference_json=$2, desired_release_id=$3, current_release_id=$4,
		last_healthy_release_id=$5, actual_status=$6, last_health_check_at=$7, config_revision=$8,
		enabled=$9, updated_at=$10
		WHERE id=$11`,
		nullString(t.ConfigProfileID), nullString(t.OverrideReferenceJSON), nullString(t.DesiredReleaseID),
		nullString(t.CurrentReleaseID), nullString(t.LastHealthyReleaseID), t.ActualStatus,
		nullTime(t.LastHealthCheckAt), t.ConfigRevision, boolInt(t.Enabled), ts(t.UpdatedAt), t.ID)
	return err
}

// DeleteDeploymentTarget removes a target.
func (s *Store) DeleteDeploymentTarget(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deployment_target WHERE id=$1`, id)
	return err
}

// ---- deployment_target_secret ----

const deploymentTargetSecretColumns = `id, target_id, secret_reference_id, binding_path, version, content_hash,
	encryption_mode, updated_at`

func scanDeploymentTargetSecret(row interface{ Scan(...any) error }) (*model.DeploymentTargetSecret, error) {
	var t model.DeploymentTargetSecret
	var updated sql.NullString
	if err := row.Scan(&t.ID, &t.TargetID, &t.SecretReferenceID, &t.BindingPath, &t.Version, &t.ContentHash,
		&t.EncryptionMode, &updated); err != nil {
		return nil, err
	}
	var err error
	if t.UpdatedAt, err = parseTimeVal(updated); err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateDeploymentTargetSecret binds a secret reference to a target; a
// duplicate (target_id, secret_reference_id) returns ErrConflict.
func (s *Store) CreateDeploymentTargetSecret(ctx context.Context, t *model.DeploymentTargetSecret) error {
	if t.ID == "" {
		t.ID = model.NewUUID()
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployment_target_secret
		(id, target_id, secret_reference_id, binding_path, version, content_hash, encryption_mode, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		t.ID, t.TargetID, t.SecretReferenceID, t.BindingPath, t.Version, t.ContentHash,
		t.EncryptionMode, ts(t.UpdatedAt))
	return conflict(err)
}

// ListDeploymentTargetSecretsByTarget returns the secret bindings of a target.
func (s *Store) ListDeploymentTargetSecretsByTarget(ctx context.Context, targetID string) ([]*model.DeploymentTargetSecret, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentTargetSecretColumns+` FROM deployment_target_secret WHERE target_id=$1 ORDER BY updated_at, id`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.DeploymentTargetSecret{}
	for rows.Next() {
		t, err := scanDeploymentTargetSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteDeploymentTargetSecretsByTarget removes all bindings of a target.
func (s *Store) DeleteDeploymentTargetSecretsByTarget(ctx context.Context, targetID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM deployment_target_secret WHERE target_id=$1`, targetID)
	return err
}

// ---- deployment_operation ----

const deploymentOperationColumns = `id, action, feature_id, release_id, strategy, status, requested_by, reason,
	environment_id, frozen_config_hash, backup_id, force_delete, created_at, started_at, finished_at`

func scanDeploymentOperation(row interface{ Scan(...any) error }) (*model.DeploymentOperation, error) {
	var o model.DeploymentOperation
	var releaseID, reason, frozenCfg, backupID, created, started, finished sql.NullString
	var forceDelete int
	if err := row.Scan(&o.ID, &o.Action, &o.FeatureID, &releaseID, &o.Strategy, &o.Status, &o.RequestedBy, &reason,
		&o.EnvironmentID, &frozenCfg, &backupID, &forceDelete, &created, &started, &finished); err != nil {
		return nil, err
	}
	o.ReleaseID = releaseID.String
	o.Reason = reason.String
	o.FrozenConfigHash = frozenCfg.String
	o.BackupID = backupID.String
	o.ForceDelete = forceDelete != 0
	var err error
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

// CreateDeploymentOperation inserts an operation. The partial unique index
// uq_deployment_op_active_feature rejects a second active operation for the
// same feature with ErrConflict.
func (s *Store) CreateDeploymentOperation(ctx context.Context, o *model.DeploymentOperation) error {
	if o.ID == "" {
		o.ID = model.NewUUID()
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployment_operation
		(id, action, feature_id, release_id, strategy, status, requested_by, reason,
		 environment_id, frozen_config_hash, backup_id, force_delete, created_at, started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		o.ID, o.Action, o.FeatureID, nullString(o.ReleaseID), o.Strategy, o.Status, o.RequestedBy, nullString(o.Reason),
		o.EnvironmentID, nullString(o.FrozenConfigHash), nullString(o.BackupID), boolInt(o.ForceDelete), ts(o.CreatedAt), nullTime(o.StartedAt), nullTime(o.FinishedAt))
	return conflict(err)
}

// CreateDeploymentOperationBundle atomically inserts an operation, its
// targets and their steps inside one transaction. Any failure rolls the whole
// bundle back, so a partial operation never persists (for example a
// node-serial conflict on the second target must not leave the operation row
// behind).
func (s *Store) CreateDeploymentOperationBundle(ctx context.Context, op *model.DeploymentOperation, targets []*model.DeploymentOperationTarget, steps []*model.DeploymentStep) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		tsx := s.Tx(tx)
		if op.ID == "" {
			op.ID = model.NewUUID()
		}
		if op.CreatedAt.IsZero() {
			op.CreatedAt = now()
		}
		if _, err := tsx.exec(ctx, `INSERT INTO deployment_operation
			(id, action, feature_id, release_id, strategy, status, requested_by, reason,
			 environment_id, frozen_config_hash, backup_id, force_delete, created_at, started_at, finished_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			op.ID, op.Action, op.FeatureID, nullString(op.ReleaseID), op.Strategy, op.Status, op.RequestedBy, nullString(op.Reason),
			op.EnvironmentID, nullString(op.FrozenConfigHash), nullString(op.BackupID), boolInt(op.ForceDelete), ts(op.CreatedAt), nullTime(op.StartedAt), nullTime(op.FinishedAt)); err != nil {
			return conflict(err)
		}
		createdAt := ts(now())
		for _, ot := range targets {
			if ot.ID == "" {
				ot.ID = model.NewUUID()
			}
			if _, err := tsx.exec(ctx, `INSERT INTO deployment_operation_target
				(id, operation_id, target_id, node_id, status, current_release_id,
				 desired_release_id, frozen_config_hash, frozen_secret_hash, error_message, started_at, finished_at, created_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
				ot.ID, ot.OperationID, ot.TargetID, ot.NodeID, ot.Status, nullString(ot.CurrentReleaseID),
				nullString(ot.DesiredReleaseID), nullString(ot.FrozenConfigHash), nullString(ot.FrozenSecretHash),
				nullString(ot.ErrorMessage), nullTime(ot.StartedAt), nullTime(ot.FinishedAt), createdAt); err != nil {
				return conflict(err)
			}
		}
		for _, st := range steps {
			if st.ID == "" {
				st.ID = model.NewUUID()
			}
			if _, err := tsx.exec(ctx, `INSERT INTO deployment_step
				(id, operation_id, operation_target_id, node_id, step_type, status, command_id,
				 task_id, message, started_at, finished_at, created_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
				st.ID, st.OperationID, st.OperationTargetID, st.NodeID, st.StepType, st.Status, nullString(st.CommandID),
				nullString(st.TaskID), nullString(st.Message), nullTime(st.StartedAt), nullTime(st.FinishedAt), createdAt); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeploymentOperationByID finds an operation.
func (s *Store) DeploymentOperationByID(ctx context.Context, id string) (*model.DeploymentOperation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deploymentOperationColumns+` FROM deployment_operation WHERE id=$1`, id)
	o, err := scanDeploymentOperation(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return o, nil
}

// ListDeploymentOperations returns operations newest first; limit<=0 defaults
// to 100.
func (s *Store) ListDeploymentOperations(ctx context.Context, limit int) ([]*model.DeploymentOperation, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentOperationColumns+` FROM deployment_operation ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.DeploymentOperation{}
	for rows.Next() {
		o, err := scanDeploymentOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// UpdateDeploymentOperation persists editable operation fields.
func (s *Store) UpdateDeploymentOperation(ctx context.Context, o *model.DeploymentOperation) error {
	_, err := s.db.ExecContext(ctx, `UPDATE deployment_operation SET
		action=$1, release_id=$2, strategy=$3, status=$4, requested_by=$5, reason=$6,
		environment_id=$7, frozen_config_hash=$8, backup_id=$9, force_delete=$10, started_at=$11, finished_at=$12
		WHERE id=$13`,
		o.Action, nullString(o.ReleaseID), o.Strategy, o.Status, o.RequestedBy, nullString(o.Reason),
		o.EnvironmentID, nullString(o.FrozenConfigHash), nullString(o.BackupID), boolInt(o.ForceDelete), nullTime(o.StartedAt), nullTime(o.FinishedAt), o.ID)
	return err
}

// ClaimQueuedDeploymentOperation atomically claims the oldest queued
// operation: within a transaction it updates the earliest queued row to
// 'running' and returns it. With concurrent claimers exactly one succeeds;
// the others receive ErrNotFound. The single UPDATE with a subquery keeps the
// claim safe on both SQLite and PostgreSQL (the subquery is re-evaluated
// under the write lock, so a stale read snapshot cannot win).
func (s *Store) ClaimQueuedDeploymentOperation(ctx context.Context) (*model.DeploymentOperation, error) {
	var claimed *model.DeploymentOperation
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		tsx := s.Tx(tx)
		var id string
		err := tsx.queryRow(ctx, `UPDATE deployment_operation
			SET status=$1, started_at=$2
			WHERE id = (SELECT id FROM deployment_operation WHERE status=$3 ORDER BY created_at, id LIMIT 1)
			RETURNING id`,
			model.DeploymentStatusRunning, ts(now()), model.DeploymentStatusQueued).Scan(&id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var scanErr error
		claimed, scanErr = scanDeploymentOperation(tsx.queryRow(ctx,
			`SELECT `+deploymentOperationColumns+` FROM deployment_operation WHERE id=$1`, id))
		return scanErr
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// ActiveDeploymentOperationForFeature returns the active operation for a
// feature (queued/running/awaiting_confirmation), or ErrNotFound when none.
// The partial unique index guarantees at most one such row per feature.
func (s *Store) ActiveDeploymentOperationForFeature(ctx context.Context, featureID string) (*model.DeploymentOperation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deploymentOperationColumns+` FROM deployment_operation
		WHERE feature_id=$1 AND status IN ($2,$3,$4)
		ORDER BY created_at, id LIMIT 1`,
		featureID, model.DeploymentStatusQueued, model.DeploymentStatusRunning, model.DeploymentStatusAwaitingConfirmation)
	o, err := scanDeploymentOperation(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return o, nil
}

// ---- deployment_operation_target ----

const deploymentOperationTargetColumns = `id, operation_id, target_id, node_id, status, current_release_id,
	desired_release_id, frozen_config_hash, frozen_secret_hash, error_message, started_at, finished_at`

func scanDeploymentOperationTarget(row interface{ Scan(...any) error }) (*model.DeploymentOperationTarget, error) {
	var t model.DeploymentOperationTarget
	var currentRel, desiredRel, frozenCfg, frozenSec, errMsg, started, finished sql.NullString
	if err := row.Scan(&t.ID, &t.OperationID, &t.TargetID, &t.NodeID, &t.Status, &currentRel,
		&desiredRel, &frozenCfg, &frozenSec, &errMsg, &started, &finished); err != nil {
		return nil, err
	}
	t.CurrentReleaseID = currentRel.String
	t.DesiredReleaseID = desiredRel.String
	t.FrozenConfigHash = frozenCfg.String
	t.FrozenSecretHash = frozenSec.String
	t.ErrorMessage = errMsg.String
	var err error
	if t.StartedAt, err = parseTime(started); err != nil {
		return nil, err
	}
	if t.FinishedAt, err = parseTime(finished); err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateDeploymentOperationTarget inserts an operation target. The partial
// unique index uq_deployment_optarget_active_node (migration 0008) rejects a
// second queued/running target for the same node with ErrConflict; a
// duplicate (operation_id, target_id) also maps to ErrConflict.
func (s *Store) CreateDeploymentOperationTarget(ctx context.Context, t *model.DeploymentOperationTarget) error {
	if t.ID == "" {
		t.ID = model.NewUUID()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployment_operation_target
		(id, operation_id, target_id, node_id, status, current_release_id,
		 desired_release_id, frozen_config_hash, frozen_secret_hash, error_message, started_at, finished_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		t.ID, t.OperationID, t.TargetID, t.NodeID, t.Status, nullString(t.CurrentReleaseID),
		nullString(t.DesiredReleaseID), nullString(t.FrozenConfigHash), nullString(t.FrozenSecretHash),
		nullString(t.ErrorMessage), nullTime(t.StartedAt), nullTime(t.FinishedAt), ts(now()))
	return conflict(err)
}

// DeploymentOperationTargetByID finds an operation target.
func (s *Store) DeploymentOperationTargetByID(ctx context.Context, id string) (*model.DeploymentOperationTarget, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deploymentOperationTargetColumns+` FROM deployment_operation_target WHERE id=$1`, id)
	t, err := scanDeploymentOperationTarget(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return t, nil
}

// ListDeploymentOperationTargetsByOperation returns the targets of an
// operation in creation order, using the created_at column (migration 0008)
// with a rowid/id tiebreak. The final stable sort by created_at preserves the
// query order for equal timestamps.
func (s *Store) ListDeploymentOperationTargetsByOperation(ctx context.Context, operationID string) ([]*model.DeploymentOperationTarget, error) {
	order := "created_at, rowid"
	if s.db.Driver == "postgres" {
		order = "created_at, id"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentOperationTargetColumns+`, created_at FROM deployment_operation_target
		WHERE operation_id=$1 ORDER BY `+order, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type targetRow struct {
		t       *model.DeploymentOperationTarget
		created string
	}
	items := []targetRow{}
	for rows.Next() {
		var created string
		var t model.DeploymentOperationTarget
		var currentRel, desiredRel, frozenCfg, frozenSec, errMsg, started, finished sql.NullString
		if err := rows.Scan(&t.ID, &t.OperationID, &t.TargetID, &t.NodeID, &t.Status, &currentRel,
			&desiredRel, &frozenCfg, &frozenSec, &errMsg, &started, &finished, &created); err != nil {
			return nil, err
		}
		t.CurrentReleaseID = currentRel.String
		t.DesiredReleaseID = desiredRel.String
		t.FrozenConfigHash = frozenCfg.String
		t.FrozenSecretHash = frozenSec.String
		t.ErrorMessage = errMsg.String
		if t.StartedAt, err = parseTime(started); err != nil {
			return nil, err
		}
		if t.FinishedAt, err = parseTime(finished); err != nil {
			return nil, err
		}
		items = append(items, targetRow{t: &t, created: created})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].created < items[j].created })
	out := make([]*model.DeploymentOperationTarget, 0, len(items))
	for _, it := range items {
		out = append(out, it.t)
	}
	return out, nil
}

// UpdateDeploymentOperationTarget persists editable operation target fields.
func (s *Store) UpdateDeploymentOperationTarget(ctx context.Context, t *model.DeploymentOperationTarget) error {
	_, err := s.db.ExecContext(ctx, `UPDATE deployment_operation_target SET
		status=$1, current_release_id=$2, desired_release_id=$3, frozen_config_hash=$4,
		frozen_secret_hash=$5, error_message=$6, started_at=$7, finished_at=$8
		WHERE id=$9`,
		t.Status, nullString(t.CurrentReleaseID), nullString(t.DesiredReleaseID), nullString(t.FrozenConfigHash),
		nullString(t.FrozenSecretHash), nullString(t.ErrorMessage), nullTime(t.StartedAt), nullTime(t.FinishedAt), t.ID)
	return err
}

// ---- deployment_step ----

const deploymentStepColumns = `id, operation_id, operation_target_id, node_id, step_type, status, command_id,
	task_id, message, started_at, finished_at`

func scanDeploymentStep(row interface{ Scan(...any) error }) (*model.DeploymentStep, error) {
	var st model.DeploymentStep
	var commandID, taskID, msg, started, finished sql.NullString
	if err := row.Scan(&st.ID, &st.OperationID, &st.OperationTargetID, &st.NodeID, &st.StepType, &st.Status,
		&commandID, &taskID, &msg, &started, &finished); err != nil {
		return nil, err
	}
	st.CommandID = commandID.String
	st.TaskID = taskID.String
	st.Message = msg.String
	var err error
	if st.StartedAt, err = parseTime(started); err != nil {
		return nil, err
	}
	if st.FinishedAt, err = parseTime(finished); err != nil {
		return nil, err
	}
	return &st, nil
}

// CreateDeploymentStep inserts a step.
func (s *Store) CreateDeploymentStep(ctx context.Context, st *model.DeploymentStep) error {
	if st.ID == "" {
		st.ID = model.NewUUID()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployment_step
		(id, operation_id, operation_target_id, node_id, step_type, status, command_id,
		 task_id, message, started_at, finished_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		st.ID, st.OperationID, st.OperationTargetID, st.NodeID, st.StepType, st.Status, nullString(st.CommandID),
		nullString(st.TaskID), nullString(st.Message), nullTime(st.StartedAt), nullTime(st.FinishedAt), ts(now()))
	return err
}

// ListDeploymentStepsByOperation returns the steps of an operation in
// creation order using the created_at column (migration 0008), see
// ListDeploymentOperationTargetsByOperation.
func (s *Store) ListDeploymentStepsByOperation(ctx context.Context, operationID string) ([]*model.DeploymentStep, error) {
	order := "created_at, rowid"
	if s.db.Driver == "postgres" {
		order = "created_at, id"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+deploymentStepColumns+`, created_at FROM deployment_step
		WHERE operation_id=$1 ORDER BY `+order, operationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type stepRow struct {
		st      *model.DeploymentStep
		created string
	}
	items := []stepRow{}
	for rows.Next() {
		var created string
		var st model.DeploymentStep
		var commandID, taskID, msg, started, finished sql.NullString
		if err := rows.Scan(&st.ID, &st.OperationID, &st.OperationTargetID, &st.NodeID, &st.StepType, &st.Status,
			&commandID, &taskID, &msg, &started, &finished, &created); err != nil {
			return nil, err
		}
		st.CommandID = commandID.String
		st.TaskID = taskID.String
		st.Message = msg.String
		if st.StartedAt, err = parseTime(started); err != nil {
			return nil, err
		}
		if st.FinishedAt, err = parseTime(finished); err != nil {
			return nil, err
		}
		items = append(items, stepRow{st: &st, created: created})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].created < items[j].created })
	out := make([]*model.DeploymentStep, 0, len(items))
	for _, it := range items {
		out = append(out, it.st)
	}
	return out, nil
}

// UpdateDeploymentStep persists editable step fields.
func (s *Store) UpdateDeploymentStep(ctx context.Context, st *model.DeploymentStep) error {
	_, err := s.db.ExecContext(ctx, `UPDATE deployment_step SET
		status=$1, command_id=$2, task_id=$3, message=$4, started_at=$5, finished_at=$6
		WHERE id=$7`,
		st.Status, nullString(st.CommandID), nullString(st.TaskID), nullString(st.Message),
		nullTime(st.StartedAt), nullTime(st.FinishedAt), st.ID)
	return err
}

// ---- deployment_backup ----

const deploymentBackupColumns = `id, operation_id, target_id, node_id, feature_id, backup_mode, object_key,
	size, sha256, metadata_json, status, created_at`

func scanDeploymentBackup(row interface{ Scan(...any) error }) (*model.DeploymentBackup, error) {
	var b model.DeploymentBackup
	var metadata, created sql.NullString
	if err := row.Scan(&b.ID, &b.OperationID, &b.TargetID, &b.NodeID, &b.FeatureID, &b.BackupMode, &b.ObjectKey,
		&b.Size, &b.SHA256, &metadata, &b.Status, &created); err != nil {
		return nil, err
	}
	b.MetadataJSON = metadata.String
	var err error
	if b.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	return &b, nil
}

// CreateDeploymentBackup inserts a backup artifact.
func (s *Store) CreateDeploymentBackup(ctx context.Context, b *model.DeploymentBackup) error {
	if b.ID == "" {
		b.ID = model.NewUUID()
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO deployment_backup
		(id, operation_id, target_id, node_id, feature_id, backup_mode, object_key,
		 size, sha256, metadata_json, status, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		b.ID, b.OperationID, b.TargetID, b.NodeID, b.FeatureID, b.BackupMode, b.ObjectKey,
		b.Size, b.SHA256, nullString(b.MetadataJSON), b.Status, ts(b.CreatedAt))
	return err
}

// DeploymentBackupByID finds a backup artifact.
func (s *Store) DeploymentBackupByID(ctx context.Context, id string) (*model.DeploymentBackup, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+deploymentBackupColumns+` FROM deployment_backup WHERE id=$1`, id)
	b, err := scanDeploymentBackup(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return b, nil
}

// ListDeploymentBackups returns backups newest first; empty filter values mean
// "no filter"; limit<=0 defaults to 100.
func (s *Store) ListDeploymentBackups(ctx context.Context, featureID, nodeID string, limit int) ([]*model.DeploymentBackup, error) {
	q := `SELECT ` + deploymentBackupColumns + ` FROM deployment_backup`
	conds := []string{}
	args := []any{}
	if featureID != "" {
		args = append(args, featureID)
		conds = append(conds, `feature_id=$`+strconv.Itoa(len(args)))
	}
	if nodeID != "" {
		args = append(args, nodeID)
		conds = append(conds, `node_id=$`+strconv.Itoa(len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)
	q += ` ORDER BY created_at DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.DeploymentBackup{}
	for rows.Next() {
		b, err := scanDeploymentBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UpdateDeploymentBackup persists editable backup fields.
func (s *Store) UpdateDeploymentBackup(ctx context.Context, b *model.DeploymentBackup) error {
	_, err := s.db.ExecContext(ctx, `UPDATE deployment_backup SET
		backup_mode=$1, object_key=$2, size=$3, sha256=$4, metadata_json=$5, status=$6
		WHERE id=$7`,
		b.BackupMode, b.ObjectKey, b.Size, b.SHA256, nullString(b.MetadataJSON), b.Status, b.ID)
	return err
}

// ---- bootstrap_session ----

const bootstrapSessionColumns = `id, node_id, status, token_hash, bucket, prefix, region, created_at, expires_at,
	revoked_at, last_state`

func scanBootstrapSession(row interface{ Scan(...any) error }) (*model.BootstrapSession, error) {
	var b model.BootstrapSession
	var revoked, lastState, created, expires sql.NullString
	if err := row.Scan(&b.ID, &b.NodeID, &b.Status, &b.TokenHash, &b.Bucket, &b.Prefix, &b.Region,
		&created, &expires, &revoked, &lastState); err != nil {
		return nil, err
	}
	b.LastState = lastState.String
	var err error
	if b.CreatedAt, err = parseTimeVal(created); err != nil {
		return nil, err
	}
	if b.ExpiresAt, err = parseTimeVal(expires); err != nil {
		return nil, err
	}
	if b.RevokedAt, err = parseTime(revoked); err != nil {
		return nil, err
	}
	return &b, nil
}

// CreateBootstrapSession inserts a bootstrap session. token_hash is the only
// credential material stored; the raw token is never persisted.
func (s *Store) CreateBootstrapSession(ctx context.Context, b *model.BootstrapSession) error {
	if b.ID == "" {
		b.ID = model.NewUUID()
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now()
	}
	if b.ExpiresAt.IsZero() {
		b.ExpiresAt = b.CreatedAt.Add(24 * time.Hour)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO bootstrap_session
		(id, node_id, status, token_hash, bucket, prefix, region, created_at, expires_at, revoked_at, last_state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		b.ID, b.NodeID, b.Status, b.TokenHash, b.Bucket, b.Prefix, b.Region,
		ts(b.CreatedAt), ts(b.ExpiresAt), nullTime(b.RevokedAt), nullString(b.LastState))
	return err
}

// BootstrapSessionByID finds a bootstrap session.
func (s *Store) BootstrapSessionByID(ctx context.Context, id string) (*model.BootstrapSession, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+bootstrapSessionColumns+` FROM bootstrap_session WHERE id=$1`, id)
	b, err := scanBootstrapSession(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return b, nil
}

// BootstrapSessionByTokenHash finds a bootstrap session by its token hash.
func (s *Store) BootstrapSessionByTokenHash(ctx context.Context, tokenHash string) (*model.BootstrapSession, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+bootstrapSessionColumns+` FROM bootstrap_session WHERE token_hash=$1`, tokenHash)
	b, err := scanBootstrapSession(row)
	if err != nil {
		return nil, sqlErr(err)
	}
	return b, nil
}

// ListBootstrapSessions returns all bootstrap sessions newest first.
func (s *Store) ListBootstrapSessions(ctx context.Context) ([]*model.BootstrapSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+bootstrapSessionColumns+` FROM bootstrap_session ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*model.BootstrapSession{}
	for rows.Next() {
		b, err := scanBootstrapSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UpdateBootstrapSession persists editable bootstrap session fields.
func (s *Store) UpdateBootstrapSession(ctx context.Context, b *model.BootstrapSession) error {
	_, err := s.db.ExecContext(ctx, `UPDATE bootstrap_session SET
		status=$1, bucket=$2, prefix=$3, region=$4, expires_at=$5, revoked_at=$6, last_state=$7
		WHERE id=$8`,
		b.Status, b.Bucket, b.Prefix, b.Region, ts(b.ExpiresAt), nullTime(b.RevokedAt), nullString(b.LastState), b.ID)
	return err
}

// RevokeBootstrapSession marks a session cancelled and records the revoke
// time.
func (s *Store) RevokeBootstrapSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE bootstrap_session SET revoked_at=$1, status=$2 WHERE id=$3`,
		ts(now()), model.BootstrapStatusCancelled, id)
	return err
}
