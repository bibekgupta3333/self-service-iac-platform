package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/auth"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/identity"
)

// CatalogService is the catalog use-case port consumed by CatalogHandler.
type CatalogService interface {
	ListModules(ctx context.Context, f catalog.ModuleFilter) ([]catalog.ModuleSummary, error)
	GetModule(ctx context.Context, name string) (catalog.ModuleSummary, error)
	ListVersions(ctx context.Context, name string) ([]catalog.ModuleVersion, error)
	GetVersion(ctx context.Context, name, version string) (catalog.ModuleVersion, error)
	Index(ctx context.Context, by identity.Principal, cmd catalog.IndexCommand) (catalog.ModuleVersion, bool, error)
	SourceAddress(v catalog.ModuleVersion) string
}

// CatalogHandler serves /v1/catalog and /v1/internal/catalog.
type CatalogHandler struct{ svc CatalogService }

// NewCatalogHandler returns a CatalogHandler.
func NewCatalogHandler(svc CatalogService) *CatalogHandler { return &CatalogHandler{svc: svc} }

// Routes registers the public catalog routes on r.
func (h *CatalogHandler) Routes(r chi.Router) {
	r.Get("/catalog/modules", h.listModules)
	r.Get("/catalog/modules/{name}", h.getModule)
	r.Get("/catalog/modules/{name}/versions", h.listVersions)
	r.Get("/catalog/modules/{name}/versions/{version}", h.getVersion)
}

// InternalRoutes registers the routes called by GitHub Actions. Only module-release.yml
// on main (or the shared token) may index.
func (h *CatalogHandler) InternalRoutes(r chi.Router) {
	r.With(requireRoute(auth.RouteCatalogIndex)).Post("/catalog/index", h.index)
}

type moduleDTO struct {
	Name          string      `json:"name"`
	DisplayName   string      `json:"display_name"`
	Description   string      `json:"description"`
	Category      string      `json:"category"`
	Owners        []string    `json:"owners"`
	RepoPath      string      `json:"repo_path"`
	Status        string      `json:"status"`
	LatestVersion *string     `json:"latest_version"`
	Latest        *versionDTO `json:"latest,omitempty"`
}

type versionSummaryDTO struct {
	Version         string    `json:"version"`
	GitTag          string    `json:"git_tag"`
	Status          string    `json:"status"`
	DeprecationNote string    `json:"deprecation_note,omitempty"`
	ReleasedAt      time.Time `json:"released_at"`
}

type versionDTO struct {
	Module           string           `json:"module"`
	Version          string           `json:"version"`
	GitTag           string           `json:"git_tag"`
	GitSHA           string           `json:"git_sha"`
	Status           string           `json:"status"`
	Source           string           `json:"source"`
	TFVersionRange   string           `json:"tf_version_range"`
	AWSProviderRange string           `json:"aws_provider_range"`
	InputsSchema     json.RawMessage  `json:"inputs_schema"`
	Outputs          []catalog.Output `json:"outputs"`
	CostHints        json.RawMessage  `json:"cost_hints"`
	ReleasedAt       time.Time        `json:"released_at"`
}

func (h *CatalogHandler) toVersionDTO(v catalog.ModuleVersion) versionDTO {
	costHints := v.CostHints
	if len(costHints) == 0 {
		costHints = json.RawMessage("null")
	}
	return versionDTO{
		Module: v.ModuleName, Version: v.Version.String(), GitTag: v.GitTag, GitSHA: v.GitSHA,
		Status: string(v.Status), Source: h.svc.SourceAddress(v), TFVersionRange: v.TFVersionRange,
		AWSProviderRange: v.AWSProviderRange, InputsSchema: v.InputsSchema, Outputs: v.Outputs,
		CostHints: costHints, ReleasedAt: v.ReleasedAt.UTC(),
	}
}

func toModuleDTO(s catalog.ModuleSummary) moduleDTO {
	d := moduleDTO{
		Name: s.Module.Name, DisplayName: s.Module.DisplayName, Description: s.Module.Description,
		Category: s.Module.Category, Owners: s.Module.Owners, RepoPath: s.Module.RepoPath, Status: string(s.Module.Status),
	}
	if s.Latest != nil {
		v := s.Latest.Version.String()
		d.LatestVersion = &v
	}
	return d
}

func toVersionSummaryDTO(v catalog.ModuleVersion) versionSummaryDTO {
	return versionSummaryDTO{Version: v.Version.String(), GitTag: v.GitTag, Status: string(v.Status),
		DeprecationNote: v.DeprecationNote, ReleasedAt: v.ReleasedAt.UTC()}
}

func (h *CatalogHandler) listModules(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mods, err := h.svc.ListModules(r.Context(), catalog.ModuleFilter{
		Category: q.Get("category"), Status: catalog.Status(q.Get("status")),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, List[moduleDTO]{Data: fp.Map(mods, toModuleDTO)})
}

func (h *CatalogHandler) getModule(w http.ResponseWriter, r *http.Request) {
	m, err := h.svc.GetModule(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	d := toModuleDTO(m)
	if m.Latest != nil {
		v := h.toVersionDTO(*m.Latest)
		d.Latest = &v
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *CatalogHandler) listVersions(w http.ResponseWriter, r *http.Request) {
	vs, err := h.svc.ListVersions(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, List[versionSummaryDTO]{Data: fp.Map(vs, toVersionSummaryDTO)})
}

func (h *CatalogHandler) getVersion(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.GetVersion(r.Context(), chi.URLParam(r, "name"), chi.URLParam(r, "version"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.toVersionDTO(v))
}

func (h *CatalogHandler) index(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var cmd catalog.IndexCommand
	if err := decodeStrict(body, &cmd); err != nil {
		writeError(w, r, err)
		return
	}
	p, err := identity.FromContext(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	v, created, err := h.svc.Index(r.Context(), p, cmd)
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, h.toVersionDTO(v))
}
