package api

import (
	"net/http"
	"sort"
)

// Auth kinds used by the API directory / OpenAPI output.
const (
	AuthNone    = "none"
	AuthAdmin   = "admin"   // cookie session + CSRF
	AuthAgent   = "agent"   // node credential + HMAC signature
	AuthToken   = "token"   // access token (sct_*)
	AuthRuntime = "runtime" // lease runtime signed token
	// AuthAdminOrToken marks read-only endpoints that accept either an admin
	// session cookie or a valid access token (dual auth).
	AuthAdminOrToken = "admin|token"
)

// RouteParam describes one documented parameter.
type RouteParam struct {
	Name        string `json:"name"`
	In          string `json:"in"` // path | query | header | body
	Type        string `json:"type"`
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
}

// RouteSpec is the single source of truth for route registration and the
// OpenAPI directory: every registered route has a spec, and the directory
// endpoint is generated from the same list so docs cannot drift.
type RouteSpec struct {
	Method   string       `json:"method"`
	Path     string       `json:"path"`
	Group    string       `json:"group"`
	Auth     string       `json:"auth"`
	Summary  string       `json:"summary,omitempty"`
	Params   []RouteParam `json:"params,omitempty"`
	Body     string       `json:"body,omitempty"`
	Response string       `json:"response,omitempty"`
	Errors   []string     `json:"errors,omitempty"`
	Debug    bool         `json:"debug"`
}

// register wires a handler into the mux and records its spec so the OpenAPI
// directory stays in sync with the actual routes.
func (s *Server) register(mux *http.ServeMux, spec RouteSpec, h http.HandlerFunc) {
	s.routes = append(s.routes, spec)
	mux.HandleFunc(spec.Method+" "+spec.Path, h)
}

// apiRoutes returns a copy of the registered route specs (stable order).
func (s *Server) apiRoutes() []RouteSpec {
	out := append([]RouteSpec(nil), s.routes...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Method+" "+out[i].Path < out[j].Method+" "+out[j].Path
	})
	return out
}
