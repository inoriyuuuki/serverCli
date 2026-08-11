package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"servercli/internal/model"
	"servercli/internal/opsv2"
	"servercli/internal/service"
	"servercli/internal/store"
)

const declarativeDefaultLimit = 100

type clusterCreateInput struct {
	Name                string              `json:"name"`
	Environment         string              `json:"environment"`
	ActivePrimaryNodeID string              `json:"active_primary_node_id,omitempty"`
	PrimaryEpoch        int64               `json:"primary_epoch"`
	ReleaseChannel      string              `json:"release_channel,omitempty"`
	OSSProviderRef      string              `json:"oss_provider_ref,omitempty"`
	UpdatePolicy        *model.UpdatePolicy `json:"update_policy,omitempty"`
	BackupPolicy        *model.BackupPolicy `json:"backup_policy,omitempty"`
	Status              string              `json:"status,omitempty"`
}

type clusterPatchInput struct {
	Name                *string             `json:"name"`
	Environment         *string             `json:"environment"`
	ActivePrimaryNodeID *string             `json:"active_primary_node_id"`
	PrimaryEpoch        *int64              `json:"primary_epoch"`
	ReleaseChannel      *string             `json:"release_channel"`
	OSSProviderRef      *string             `json:"oss_provider_ref"`
	UpdatePolicy        *model.UpdatePolicy `json:"update_policy"`
	BackupPolicy        *model.BackupPolicy `json:"backup_policy"`
	Status              *string             `json:"status"`
}

type clusterView struct {
	*model.Cluster
	UpdatePolicy *model.UpdatePolicy `json:"update_policy,omitempty"`
	BackupPolicy *model.BackupPolicy `json:"backup_policy,omitempty"`
}

func newClusterView(c *model.Cluster) (clusterView, error) {
	view := clusterView{Cluster: c}
	if c.UpdatePolicyJSON != "" {
		var policy model.UpdatePolicy
		if err := json.Unmarshal([]byte(c.UpdatePolicyJSON), &policy); err != nil {
			return clusterView{}, err
		}
		view.UpdatePolicy = &policy
	}
	if c.BackupPolicyJSON != "" {
		var policy model.BackupPolicy
		if err := json.Unmarshal([]byte(c.BackupPolicyJSON), &policy); err != nil {
			return clusterView{}, err
		}
		view.BackupPolicy = &policy
	}
	return view, nil
}

func (s *Server) handleListClusters(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListClusters(r.Context(), r.URL.Query().Get("environment"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	out := make([]clusterView, 0, len(rows))
	for _, row := range rows {
		view, err := newClusterView(row)
		if err != nil {
			s.writeDeclarativeStoreError(w, r, err)
			return
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"clusters": out})
}

func (s *Server) handleCreateCluster(w http.ResponseWriter, r *http.Request) {
	var in clusterCreateInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Environment) == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "name and environment are required", nil)
		return
	}
	c := &model.Cluster{
		ID: model.NewUUID(), Name: strings.TrimSpace(in.Name), Environment: strings.TrimSpace(in.Environment),
		ActivePrimaryNodeID: in.ActivePrimaryNodeID, PrimaryEpoch: in.PrimaryEpoch,
		ReleaseChannel: in.ReleaseChannel, OSSProviderRef: in.OSSProviderRef, Status: in.Status,
	}
	var err error
	if c.UpdatePolicyJSON, err = marshalOptionalJSON(in.UpdatePolicy); err != nil {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid update_policy", nil)
		return
	}
	if c.BackupPolicyJSON, err = marshalOptionalJSON(in.BackupPolicy); err != nil {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid backup_policy", nil)
		return
	}
	if err := s.store.CreateCluster(r.Context(), c); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	view, err := newClusterView(c)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"cluster": view})
}

func (s *Server) handleGetCluster(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.ClusterByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	view, err := newClusterView(c)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cluster": view})
}

func (s *Server) handlePatchCluster(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.ClusterByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	var in clusterPatchInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if in.Environment != nil {
		c.Environment = strings.TrimSpace(*in.Environment)
	}
	if c.Name == "" || c.Environment == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "name and environment cannot be empty", nil)
		return
	}
	if in.ActivePrimaryNodeID != nil {
		c.ActivePrimaryNodeID = *in.ActivePrimaryNodeID
	}
	if in.PrimaryEpoch != nil {
		c.PrimaryEpoch = *in.PrimaryEpoch
	}
	if in.ReleaseChannel != nil {
		c.ReleaseChannel = *in.ReleaseChannel
	}
	if in.OSSProviderRef != nil {
		c.OSSProviderRef = *in.OSSProviderRef
	}
	if in.UpdatePolicy != nil {
		c.UpdatePolicyJSON, err = marshalOptionalJSON(in.UpdatePolicy)
		if err != nil {
			writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid update_policy", nil)
			return
		}
	}
	if in.BackupPolicy != nil {
		c.BackupPolicyJSON, err = marshalOptionalJSON(in.BackupPolicy)
		if err != nil {
			writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid backup_policy", nil)
			return
		}
	}
	if in.Status != nil {
		c.Status = *in.Status
	}
	if err := s.store.UpdateCluster(r.Context(), c); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	view, err := newClusterView(c)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cluster": view})
}

func (s *Server) handleDeleteCluster(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	profiles, err := s.store.ListNodeProfiles(r.Context(), id)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	nodes, err := s.store.ListDeclarativeNodes(r.Context(), id, "")
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	references, err := s.store.ListServiceReferences(r.Context(), id)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	operations, err := s.store.ListOperations(r.Context(), id, "", "", 1, 0)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	transfers, err := s.store.ListPrimaryTransfers(r.Context(), id)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	if len(profiles)+len(nodes)+len(references)+len(operations)+len(transfers) > 0 {
		writeError(w, r, s.log, http.StatusConflict, "CONFLICT", "cluster still has declarative resources", nil)
		return
	}
	if err := s.store.DeleteCluster(r.Context(), id); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

type profileInput struct {
	Name               string                `json:"name"`
	Version            string                `json:"version,omitempty"`
	Modules            []model.ProfileModule `json:"modules"`
	DefaultConfig      json.RawMessage       `json:"default_config,omitempty"`
	SecretRefs         []model.SecretRef     `json:"secret_refs,omitempty"`
	BackupPolicy       json.RawMessage       `json:"backup_policy,omitempty"`
	UpdatePolicy       json.RawMessage       `json:"update_policy,omitempty"`
	VerificationPolicy json.RawMessage       `json:"verification_policy,omitempty"`
	Labels             json.RawMessage       `json:"labels,omitempty"`
	Resources          json.RawMessage       `json:"resources,omitempty"`
	Status             string                `json:"status,omitempty"`
}

type profilePatchInput struct {
	Name               *string                `json:"name"`
	Version            *string                `json:"version"`
	Modules            *[]model.ProfileModule `json:"modules"`
	DefaultConfig      json.RawMessage        `json:"default_config"`
	SecretRefs         *[]model.SecretRef     `json:"secret_refs"`
	BackupPolicy       json.RawMessage        `json:"backup_policy"`
	UpdatePolicy       json.RawMessage        `json:"update_policy"`
	VerificationPolicy json.RawMessage        `json:"verification_policy"`
	Labels             json.RawMessage        `json:"labels"`
	Resources          json.RawMessage        `json:"resources"`
	Status             *string                `json:"status"`
}

type nodeProfileView struct {
	*model.NodeProfile
	Modules []model.ProfileModule `json:"modules"`
}

func newNodeProfileView(p *model.NodeProfile) (nodeProfileView, error) {
	mods, err := p.UnmarshalProfileModules()
	if err != nil {
		return nodeProfileView{}, err
	}
	if mods == nil {
		mods = []model.ProfileModule{}
	}
	return nodeProfileView{NodeProfile: p, Modules: mods}, nil
}

func (s *Server) handleListNodeProfiles(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListNodeProfiles(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	out := make([]nodeProfileView, 0, len(rows))
	for _, row := range rows {
		view, err := newNodeProfileView(row)
		if err != nil {
			s.writeDeclarativeStoreError(w, r, err)
			return
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
}

func (s *Server) handleCreateNodeProfile(w http.ResponseWriter, r *http.Request) {
	var in profileInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "name is required", nil)
		return
	}
	p := &model.NodeProfile{
		ID: model.NewUUID(), ClusterID: r.PathValue("id"), Name: strings.TrimSpace(in.Name),
		Version: in.Version, Status: in.Status,
		DefaultConfigJSON: string(in.DefaultConfig), BackupPolicyJSON: string(in.BackupPolicy),
		UpdatePolicyJSON: string(in.UpdatePolicy), VerificationPolicyJSON: string(in.VerificationPolicy),
		LabelsJSON: string(in.Labels), ResourcesJSON: string(in.Resources),
	}
	if p.Version == "" {
		p.Version = "1"
	}
	if err := p.MarshalProfileModules(in.Modules); err != nil {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid modules", nil)
		return
	}
	if in.SecretRefs != nil {
		b, err := json.Marshal(in.SecretRefs)
		if err != nil {
			writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid secret_refs", nil)
			return
		}
		p.SecretRefsJSON = string(b)
	}
	if err := s.store.CreateNodeProfile(r.Context(), p); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	view, err := newNodeProfileView(p)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"profile": view})
}

func (s *Server) handleGetNodeProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.NodeProfileByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	view, err := newNodeProfileView(p)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": view})
}

func (s *Server) handlePatchNodeProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.NodeProfileByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	var in profilePatchInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if in.Name != nil {
		p.Name = strings.TrimSpace(*in.Name)
	}
	if p.Name == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "name cannot be empty", nil)
		return
	}
	if in.Version != nil {
		p.Version = *in.Version
	}
	if in.Modules != nil {
		if err := p.MarshalProfileModules(*in.Modules); err != nil {
			writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid modules", nil)
			return
		}
	}
	setRawIfPresent(&p.DefaultConfigJSON, in.DefaultConfig)
	setRawIfPresent(&p.BackupPolicyJSON, in.BackupPolicy)
	setRawIfPresent(&p.UpdatePolicyJSON, in.UpdatePolicy)
	setRawIfPresent(&p.VerificationPolicyJSON, in.VerificationPolicy)
	setRawIfPresent(&p.LabelsJSON, in.Labels)
	setRawIfPresent(&p.ResourcesJSON, in.Resources)
	if in.SecretRefs != nil {
		b, err := json.Marshal(*in.SecretRefs)
		if err != nil {
			writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid secret_refs", nil)
			return
		}
		p.SecretRefsJSON = string(b)
	}
	if in.Status != nil {
		p.Status = *in.Status
	}
	if err := s.store.UpdateNodeProfile(r.Context(), p); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	view, err := newNodeProfileView(p)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": view})
}

type declarativeNodeInput struct {
	ClusterID          string                         `json:"cluster_id"`
	NodeID             string                         `json:"node_id"`
	Role               string                         `json:"role,omitempty"`
	ProfileID          string                         `json:"profile_id,omitempty"`
	Lifecycle          string                         `json:"lifecycle,omitempty"`
	Status             string                         `json:"status,omitempty"`
	Labels             json.RawMessage                `json:"labels,omitempty"`
	Addresses          []model.DeclarativeNodeAddress `json:"addresses"`
	OSName             string                         `json:"os_name,omitempty"`
	OSVersion          string                         `json:"os_version,omitempty"`
	Arch               string                         `json:"arch,omitempty"`
	DesiredRevision    string                         `json:"desired_revision,omitempty"`
	AppliedRevision    string                         `json:"applied_revision,omitempty"`
	IdentityGeneration int64                          `json:"identity_generation,omitempty"`
	ReplacementStatus  string                         `json:"replacement_status,omitempty"`
	AgentStatus        string                         `json:"agent_status,omitempty"`
	LegacyMAC          string                         `json:"legacy_mac,omitempty"`
}

type declarativeNodePatchInput struct {
	Role               *string                         `json:"role"`
	ProfileID          *string                         `json:"profile_id"`
	Lifecycle          *string                         `json:"lifecycle"`
	Status             *string                         `json:"status"`
	Labels             json.RawMessage                 `json:"labels"`
	Addresses          *[]model.DeclarativeNodeAddress `json:"addresses"`
	OSName             *string                         `json:"os_name"`
	OSVersion          *string                         `json:"os_version"`
	Arch               *string                         `json:"arch"`
	DesiredRevision    *string                         `json:"desired_revision"`
	AppliedRevision    *string                         `json:"applied_revision"`
	IdentityGeneration *int64                          `json:"identity_generation"`
	ReplacementStatus  *string                         `json:"replacement_status"`
	AgentStatus        *string                         `json:"agent_status"`
	LegacyMAC          *string                         `json:"legacy_mac"`
}

type declarativeNodeView struct {
	*model.DeclarativeNode
	Addresses []model.DeclarativeNodeAddress `json:"addresses"`
}

func newDeclarativeNodeView(n *model.DeclarativeNode) (declarativeNodeView, error) {
	addresses, err := n.UnmarshalNodeAddresses()
	if err != nil {
		return declarativeNodeView{}, err
	}
	if addresses == nil {
		addresses = []model.DeclarativeNodeAddress{}
	}
	return declarativeNodeView{DeclarativeNode: n, Addresses: addresses}, nil
}

func (s *Server) handleListDeclarativeNodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, err := s.store.ListDeclarativeNodes(r.Context(), q.Get("cluster_id"), q.Get("lifecycle"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	out := make([]declarativeNodeView, 0, len(rows))
	for _, row := range rows {
		view, err := newDeclarativeNodeView(row)
		if err != nil {
			s.writeDeclarativeStoreError(w, r, err)
			return
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"declarative_nodes": out})
}

func (s *Server) handleCreateDeclarativeNode(w http.ResponseWriter, r *http.Request) {
	var in declarativeNodeInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if strings.TrimSpace(in.ClusterID) == "" || strings.TrimSpace(in.NodeID) == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "cluster_id and node_id are required", nil)
		return
	}
	addresses, err := json.Marshal(in.Addresses)
	if err != nil {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid addresses", nil)
		return
	}
	n := &model.DeclarativeNode{
		ID: model.NewUUID(), ClusterID: strings.TrimSpace(in.ClusterID), NodeID: strings.TrimSpace(in.NodeID),
		Role: in.Role, ProfileID: in.ProfileID, Lifecycle: in.Lifecycle, Status: in.Status,
		LabelsJSON: string(in.Labels), AddressesJSON: string(addresses), OSName: in.OSName,
		OSVersion: in.OSVersion, Arch: in.Arch, DesiredRevision: in.DesiredRevision,
		AppliedRevision: in.AppliedRevision, IdentityGeneration: in.IdentityGeneration,
		ReplacementStatus: in.ReplacementStatus, AgentStatus: in.AgentStatus, LegacyMAC: in.LegacyMAC,
	}
	if n.Role == "" {
		n.Role = "child"
	}
	if err := s.store.CreateDeclarativeNode(r.Context(), n); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	view, err := newDeclarativeNodeView(n)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"declarative_node": view})
}

func (s *Server) handleGetDeclarativeNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.DeclarativeNodeByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	view, err := newDeclarativeNodeView(n)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"declarative_node": view})
}

func (s *Server) handlePatchDeclarativeNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.DeclarativeNodeByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	var in declarativeNodePatchInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if in.Role != nil {
		n.Role = *in.Role
	}
	if in.ProfileID != nil {
		n.ProfileID = *in.ProfileID
	}
	if in.Lifecycle != nil {
		n.Lifecycle = *in.Lifecycle
	}
	if in.Status != nil {
		n.Status = *in.Status
	}
	setRawIfPresent(&n.LabelsJSON, in.Labels)
	if in.Addresses != nil {
		b, err := json.Marshal(*in.Addresses)
		if err != nil {
			writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid addresses", nil)
			return
		}
		n.AddressesJSON = string(b)
	}
	if in.OSName != nil {
		n.OSName = *in.OSName
	}
	if in.OSVersion != nil {
		n.OSVersion = *in.OSVersion
	}
	if in.Arch != nil {
		n.Arch = *in.Arch
	}
	if in.DesiredRevision != nil {
		n.DesiredRevision = *in.DesiredRevision
	}
	if in.AppliedRevision != nil {
		n.AppliedRevision = *in.AppliedRevision
	}
	if in.IdentityGeneration != nil {
		n.IdentityGeneration = *in.IdentityGeneration
	}
	if in.ReplacementStatus != nil {
		n.ReplacementStatus = *in.ReplacementStatus
	}
	if in.AgentStatus != nil {
		n.AgentStatus = *in.AgentStatus
	}
	if in.LegacyMAC != nil {
		n.LegacyMAC = *in.LegacyMAC
	}
	if err := s.store.UpdateDeclarativeNode(r.Context(), n); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	view, err := newDeclarativeNodeView(n)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"declarative_node": view})
}

type serviceReferenceInput struct {
	Name              string           `json:"name"`
	ServiceInstanceID string           `json:"service_instance_id,omitempty"`
	NodeID            string           `json:"node_id,omitempty"`
	Address           string           `json:"address,omitempty"`
	Port              int              `json:"port,omitempty"`
	SecretRef         *model.SecretRef `json:"secret_ref,omitempty"`
	Status            string           `json:"status,omitempty"`
}

type serviceReferencePatchInput struct {
	ServiceInstanceID *string          `json:"service_instance_id"`
	NodeID            *string          `json:"node_id"`
	Address           *string          `json:"address"`
	Port              *int             `json:"port"`
	SecretRef         *model.SecretRef `json:"secret_ref"`
	Status            *string          `json:"status"`
}

func (s *Server) handleListServiceReferences(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListServiceReferences(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"service_references": rows})
}

func (s *Server) handleCreateServiceReference(w http.ResponseWriter, r *http.Request) {
	var in serviceReferenceInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "name is required", nil)
		return
	}
	ref := &model.ServiceReference{
		ID: model.NewUUID(), ClusterID: r.PathValue("id"), Name: strings.TrimSpace(in.Name),
		ServiceInstanceID: in.ServiceInstanceID, NodeID: in.NodeID, Address: in.Address,
		Port: in.Port, SecretRef: in.SecretRef, Status: in.Status,
	}
	if err := s.store.CreateServiceReference(r.Context(), ref); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"service_reference": ref})
}

func (s *Server) handleGetServiceReference(w http.ResponseWriter, r *http.Request) {
	ref, err := s.store.ServiceReferenceByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"service_reference": ref})
}

func (s *Server) handlePatchServiceReference(w http.ResponseWriter, r *http.Request) {
	ref, err := s.store.ServiceReferenceByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	var in serviceReferencePatchInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if in.ServiceInstanceID != nil {
		ref.ServiceInstanceID = *in.ServiceInstanceID
	}
	if in.NodeID != nil {
		ref.NodeID = *in.NodeID
	}
	if in.Address != nil {
		ref.Address = *in.Address
	}
	if in.Port != nil {
		ref.Port = *in.Port
	}
	if in.SecretRef != nil {
		ref.SecretRef = in.SecretRef
	}
	if in.Status != nil {
		ref.Status = *in.Status
	}
	if err := s.store.UpdateServiceReference(r.Context(), ref); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"service_reference": ref})
}

type operationView struct {
	ID                string     `json:"id"`
	OperationID       string     `json:"operation_id"`
	OperationType     string     `json:"operation_type"`
	ClusterID         string     `json:"cluster_id,omitempty"`
	NodeID            string     `json:"node_id,omitempty"`
	ModuleID          string     `json:"module_id,omitempty"`
	ServiceInstanceID string     `json:"service_instance_id,omitempty"`
	DesiredRevision   string     `json:"desired_revision,omitempty"`
	Approval          string     `json:"approval,omitempty"`
	RiskLevel         string     `json:"risk_level,omitempty"`
	Deadline          *time.Time `json:"deadline,omitempty"`
	PrimaryEpoch      int64      `json:"primary_epoch"`
	Status            string     `json:"status"`
	RequestedBy       string     `json:"requested_by,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`
	ErrorCode         string     `json:"error_code,omitempty"`
	ErrorMessage      string     `json:"error_message,omitempty"`
}

func newOperationView(o *model.Operation) operationView {
	return operationView{
		ID: o.ID, OperationID: o.OperationID, OperationType: o.OperationType,
		ClusterID: o.ClusterID, NodeID: o.NodeID, ModuleID: o.ModuleID,
		ServiceInstanceID: o.ServiceInstanceID, DesiredRevision: o.DesiredRevision,
		Approval: o.Approval, RiskLevel: o.RiskLevel, Deadline: o.Deadline,
		PrimaryEpoch: o.PrimaryEpoch, Status: o.Status, RequestedBy: o.RequestedBy,
		CreatedAt: o.CreatedAt, StartedAt: o.StartedAt, FinishedAt: o.FinishedAt,
		ErrorCode: o.ErrorCode, ErrorMessage: o.ErrorMessage,
	}
}

func (s *Server) handleCreateOperationV2(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid operation request", nil)
		return
	}
	req, err := opsv2.ParseOperationRequest(data)
	if err != nil {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid operation request", nil)
		return
	}
	if headerKey := strings.TrimSpace(r.Header.Get("Idempotency-Key")); headerKey != "" {
		req.IdempotencyKey = headerKey
	}
	if err := req.Validate(); err != nil {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid operation request", nil)
		return
	}
	requestedBy, _ := declarativePrincipal(r)
	if requestedBy == "" {
		writeError(w, r, s.log, http.StatusUnauthorized, "UNAUTHENTICATED", "missing principal", nil)
		return
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" {
		key = opsv2.IdempotencyKey(req)
		req.IdempotencyKey = key
	}
	if existing, err := s.store.OperationByIdempotency(r.Context(), requestedBy, key); err == nil {
		if !opsv2.MatchIdempotency(existing, req) {
			writeError(w, r, s.log, http.StatusConflict, "CONFLICT", "idempotency key conflicts with an existing operation", nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"operation": newOperationView(existing), "created": false})
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	cluster, err := s.store.ClusterByID(r.Context(), req.ClusterID)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	if err := opsv2.ValidateEpoch(req.PrimaryEpoch, cluster.PrimaryEpoch); err != nil {
		writeError(w, r, s.log, http.StatusConflict, "STALE_PRIMARY_EPOCH", "operation primary_epoch does not match cluster", nil)
		return
	}
	op, err := req.ToOperation(model.NewUUID(), requestedBy)
	if err != nil {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "invalid operation request", nil)
		return
	}
	if err := s.store.CreateOperation(r.Context(), op); err != nil {
		if isConstraintConflict(err) {
			if existing, getErr := s.store.OperationByIdempotency(r.Context(), requestedBy, key); getErr == nil && opsv2.MatchIdempotency(existing, req) {
				writeJSON(w, http.StatusOK, map[string]any{"operation": newOperationView(existing), "created": false})
				return
			}
		}
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"operation": newOperationView(op), "created": true})
}

func (s *Server) handleListOperationsV2(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, ok := parseNonNegativeQuery(w, r, s, "limit", declarativeDefaultLimit)
	if !ok {
		return
	}
	offset, ok := parseNonNegativeQuery(w, r, s, "offset", 0)
	if !ok {
		return
	}
	rows, err := s.store.ListOperations(r.Context(), q.Get("cluster_id"), q.Get("node_id"), q.Get("status"), limit, offset)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	out := make([]operationView, 0, len(rows))
	for _, row := range rows {
		out = append(out, newOperationView(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"operations": out})
}

func (s *Server) handleGetOperationV2(w http.ResponseWriter, r *http.Request) {
	op, err := s.store.OperationByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	steps, err := s.store.ListOperationSteps(r.Context(), op.ID)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"operation": newOperationView(op), "steps": steps})
}

func (s *Server) handleApproveOperationV2(w http.ResponseWriter, r *http.Request) {
	op, err := s.store.OperationByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	if op.Status != model.OpStatusPlanned || !opsv2.CanTransition(op.Status, model.OpStatusQueued) {
		writeError(w, r, s.log, http.StatusConflict, "INVALID_TRANSITION", "operation cannot be approved from its current status", nil)
		return
	}
	if err := s.store.UpdateOperationStatus(r.Context(), op.ID, model.OpStatusQueued, "", ""); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	op.Status = model.OpStatusQueued
	admin := adminFrom(r.Context())
	_ = s.auditor.OK(r.Context(), service.AuditInput{
		ActorType: model.ActorAdmin, ActorID: admin.ID, Action: "operation.approve",
		ResourceType: "operation_v2", ResourceID: op.ID, Summary: "declarative operation approved",
		RiskLevel: service.RiskHigh,
	})
	writeJSON(w, http.StatusOK, map[string]any{"operation": newOperationView(op)})
}

func (s *Server) handleCancelOperationV2(w http.ResponseWriter, r *http.Request) {
	op, err := s.store.OperationByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	if op.Status != model.OpStatusPlanned || !opsv2.CanTransition(op.Status, model.OpStatusCancelled) {
		writeError(w, r, s.log, http.StatusConflict, "INVALID_TRANSITION", "operation cannot be cancelled from its current status", nil)
		return
	}
	if err := s.store.UpdateOperationStatus(r.Context(), op.ID, model.OpStatusCancelled, "", ""); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	op.Status = model.OpStatusCancelled
	admin := adminFrom(r.Context())
	_ = s.auditor.OK(r.Context(), service.AuditInput{
		ActorType: model.ActorAdmin, ActorID: admin.ID, Action: "operation.cancel",
		ResourceType: "operation_v2", ResourceID: op.ID, Summary: "declarative operation cancelled",
		RiskLevel: service.RiskHigh,
	})
	writeJSON(w, http.StatusOK, map[string]any{"operation": newOperationView(op)})
}

func (s *Server) handleUpdateOperationStatusV2(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Status       string `json:"status"`
		ErrorCode    string `json:"error_code,omitempty"`
		ErrorMessage string `json:"error_message,omitempty"`
	}
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	op, err := s.store.OperationByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	if !opsv2.CanTransition(op.Status, in.Status) || isOperationDecisionStatus(in.Status) {
		writeError(w, r, s.log, http.StatusConflict, "INVALID_TRANSITION", "operation status transition is not allowed", nil)
		return
	}
	if err := s.store.UpdateOperationStatus(r.Context(), op.ID, in.Status, in.ErrorCode, in.ErrorMessage); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	op.Status = in.Status
	op.ErrorCode = in.ErrorCode
	op.ErrorMessage = in.ErrorMessage
	writeJSON(w, http.StatusOK, map[string]any{"operation": newOperationView(op)})
}

func isOperationDecisionStatus(status string) bool {
	switch status {
	case model.OpStatusAwaitingApproval, model.OpStatusQueued, model.OpStatusCancelled:
		return true
	default:
		return false
	}
}

type releaseCacheInput struct {
	Version          string     `json:"version"`
	SourceRepository string     `json:"source_repository,omitempty"`
	SourceRelease    string     `json:"source_release,omitempty"`
	OS               string     `json:"os,omitempty"`
	Arch             string     `json:"arch,omitempty"`
	ArtifactName     string     `json:"artifact_name"`
	ArtifactSize     int64      `json:"artifact_size,omitempty"`
	SHA256           string     `json:"sha256"`
	ModulesVersion   string     `json:"modules_version,omitempty"`
	SchemaMin        string     `json:"schema_min,omitempty"`
	SchemaMax        string     `json:"schema_max,omitempty"`
	OSSKey           string     `json:"oss_key,omitempty"`
	Status           string     `json:"status,omitempty"`
	UploadedAt       *time.Time `json:"uploaded_at,omitempty"`
	VerifiedAt       *time.Time `json:"verified_at,omitempty"`
}

func (s *Server) handleListReleaseCache(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, err := s.store.ListReleaseCacheEntries(r.Context(), q.Get("version"), q.Get("status"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"release_cache": rows})
}

func (s *Server) handleCreateReleaseCache(w http.ResponseWriter, r *http.Request) {
	var in releaseCacheInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if strings.TrimSpace(in.Version) == "" || strings.TrimSpace(in.ArtifactName) == "" || strings.TrimSpace(in.SHA256) == "" {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "version, artifact_name, and sha256 are required", nil)
		return
	}
	entry := &model.ReleaseCacheEntry{
		ID: model.NewUUID(), Version: in.Version, SourceRepository: in.SourceRepository,
		SourceRelease: in.SourceRelease, OS: in.OS, Arch: in.Arch, ArtifactName: in.ArtifactName,
		ArtifactSize: in.ArtifactSize, SHA256: in.SHA256, ModulesVersion: in.ModulesVersion,
		SchemaMin: in.SchemaMin, SchemaMax: in.SchemaMax, OSSKey: in.OSSKey, Status: in.Status,
		UploadedAt: in.UploadedAt, VerifiedAt: in.VerifiedAt,
	}
	if err := s.store.CreateReleaseCacheEntry(r.Context(), entry); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"release_cache_entry": entry})
}

func (s *Server) handleGetReleaseCache(w http.ResponseWriter, r *http.Request) {
	entry, err := s.store.ReleaseCacheEntryByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"release_cache_entry": entry})
}

func (s *Server) handleMarkReleaseCacheAvailable(w http.ResponseWriter, r *http.Request) {
	entry, err := s.store.ReleaseCacheEntryByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	now := time.Now().UTC()
	if err := s.store.UpdateReleaseCacheEntryStatus(r.Context(), entry.ID, model.ReleaseCacheAvailable, &now, &now); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	entry.Status = model.ReleaseCacheAvailable
	entry.UploadedAt = &now
	entry.VerifiedAt = &now
	writeJSON(w, http.StatusOK, map[string]any{"release_cache_entry": entry})
}

func (s *Server) handleListBackupSets(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, ok := parseNonNegativeQuery(w, r, s, "limit", declarativeDefaultLimit)
	if !ok {
		return
	}
	rows, err := s.store.ListBackupSets(r.Context(), q.Get("node_id"), q.Get("status"), limit)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backup_sets": rows})
}

func (s *Server) handleListOSSSyncs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, ok := parseNonNegativeQuery(w, r, s, "limit", declarativeDefaultLimit)
	if !ok {
		return
	}
	rows, err := s.store.ListOSSSyncRevisions(r.Context(), q.Get("kind"), limit)
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"oss_syncs": rows})
}

type primaryTransferInput struct {
	ClusterID    string          `json:"cluster_id"`
	FromNodeID   string          `json:"from_node_id"`
	ToNodeID     string          `json:"to_node_id"`
	PrimaryEpoch int64           `json:"primary_epoch"`
	BackupSetID  string          `json:"backup_set_id,omitempty"`
	Steps        json.RawMessage `json:"steps,omitempty"`
}

func (s *Server) handleListPrimaryTransfers(w http.ResponseWriter, r *http.Request) {
	clusterID := r.URL.Query().Get("cluster_id")
	if clusterID != "" {
		rows, err := s.store.ListPrimaryTransfers(r.Context(), clusterID)
		if err != nil {
			s.writeDeclarativeStoreError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"primary_transfers": rows})
		return
	}
	clusters, err := s.store.ListClusters(r.Context(), "")
	if err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	rows := make([]*model.PrimaryTransfer, 0)
	for _, cluster := range clusters {
		clusterRows, err := s.store.ListPrimaryTransfers(r.Context(), cluster.ID)
		if err != nil {
			s.writeDeclarativeStoreError(w, r, err)
			return
		}
		rows = append(rows, clusterRows...)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"primary_transfers": rows})
}

func (s *Server) handleCreatePrimaryTransfer(w http.ResponseWriter, r *http.Request) {
	var in primaryTransferInput
	if !decodeJSON(w, r, s.log, &in) {
		return
	}
	if strings.TrimSpace(in.ClusterID) == "" || strings.TrimSpace(in.FromNodeID) == "" || strings.TrimSpace(in.ToNodeID) == "" || in.PrimaryEpoch <= 0 {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", "cluster_id, from_node_id, to_node_id, and positive primary_epoch are required", nil)
		return
	}
	requestedBy, _ := declarativePrincipal(r)
	transfer := &model.PrimaryTransfer{
		ID: model.NewUUID(), ClusterID: in.ClusterID, FromNodeID: in.FromNodeID, ToNodeID: in.ToNodeID,
		PrimaryEpoch: in.PrimaryEpoch, Status: model.TransferPlanning, BackupSetID: in.BackupSetID,
		StepsJSON: string(in.Steps), RequestedBy: requestedBy,
	}
	if err := s.store.CreatePrimaryTransfer(r.Context(), transfer); err != nil {
		s.writeDeclarativeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"primary_transfer": transfer})
}

func declarativePrincipal(r *http.Request) (id, actorType string) {
	if admin := adminFrom(r.Context()); admin != nil && admin.ID != "" {
		return admin.ID, model.ActorAdmin
	}
	if principal := tokenPrincipalFrom(r.Context()); principal != nil && principal.TokenID != "" {
		return principal.TokenID, model.ActorAI
	}
	return "", ""
}

func marshalOptionalJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func setRawIfPresent(dst *string, raw json.RawMessage) {
	if len(raw) > 0 {
		*dst = string(raw)
	}
}

func parseNonNegativeQuery(w http.ResponseWriter, r *http.Request, s *Server, name string, defaultValue int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return defaultValue, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		writeError(w, r, s.log, http.StatusBadRequest, "BAD_REQUEST", name+" must be a non-negative integer", nil)
		return 0, false
	}
	return value, true
}

func (s *Server) writeDeclarativeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, r, s.log, http.StatusNotFound, "NOT_FOUND", "resource not found", nil)
	case errors.Is(err, store.ErrConflict), isConstraintConflict(err):
		writeError(w, r, s.log, http.StatusConflict, "CONFLICT", "resource conflicts with an existing record", nil)
	default:
		s.log.Error("declarative API store error", "error", err)
		writeError(w, r, s.log, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", nil)
	}
}

func isConstraintConflict(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint") || strings.Contains(text, "duplicate key")
}
