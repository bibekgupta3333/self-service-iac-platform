package generator

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
)

// Generated file names.
const (
	FileMain      = "main.tf"
	FileVersions  = "versions.tf"
	FileProviders = "providers.tf"
	FileBackend   = "backend.tf"
	FileOutputs   = "outputs.tf"
	FileVariables = "variables.tf"
	FileStackJSON = "iacp.stack.json"
)

// Files maps a file name (relative to the stack root) to its content.
type Files map[string][]byte

// Names returns the file names in sorted order.
func (f Files) Names() []string { return fp.SortedKeys(f) }

// Generator renders stack roots with a fixed configuration.
type Generator struct {
	repoURL  string
	backend  BackendStrategy
	provider ProviderStrategy
}

// New returns a Generator. repoURL is the git URL modules are sourced from.
func New(repoURL string, backend BackendStrategy, provider ProviderStrategy) *Generator {
	return &Generator{repoURL: repoURL, backend: backend, provider: provider}
}

// Render produces every file of the stack root. Same spec in, same bytes out.
func (g *Generator) Render(spec StackSpec) (Files, error) {
	res, err := prepare(spec)
	if err != nil {
		return nil, fmt.Errorf("invalid stack spec: %w", err)
	}
	id := spec.Identity()
	main, err := RenderMain(id, res, g.repoURL)
	if err != nil {
		return nil, err
	}
	providers, err := RenderProviders(spec, g.provider.Provider(id))
	if err != nil {
		return nil, err
	}
	blockType, attrs := g.backend.Backend(id)
	backend, err := RenderBackend(blockType, attrs)
	if err != nil {
		return nil, err
	}
	stackJSON, err := RenderStackJSON(spec, res, g.backend.Name())
	if err != nil {
		return nil, err
	}
	return Files{
		FileMain:      main,
		FileVersions:  RenderVersions(spec.TerraformVersion, spec.AWSProviderVersion),
		FileProviders: providers,
		FileBackend:   backend,
		FileOutputs:   RenderOutputs(res),
		FileVariables: RenderVariables(),
		FileStackJSON: stackJSON,
	}, nil
}

// RenderMain renders one module block per resource, sorted by name; the pinned source
// first, then inputs sorted by key.
func RenderMain(id StackIdentity, res []decodedResource, repoURL string) ([]byte, error) {
	f := hclwrite.NewEmptyFile()
	body := f.Body()
	for i, r := range res {
		if i > 0 {
			body.AppendNewline()
		}
		blk := body.AppendNewBlock("module", []string{r.Name}).Body()
		blk.SetAttributeValue("source", cty.StringVal(catalog.SourceAddress(repoURL, r.Module, r.Version)))
		if len(r.inputs) > 0 {
			blk.AppendNewline()
		}
		for _, k := range fp.SortedKeys(r.inputs) {
			tokens, err := valueTokens(r.inputs[k])
			if err != nil {
				return nil, fmt.Errorf("resource %s input %s: %w", r.Name, k, err)
			}
			blk.SetAttributeRaw(k, tokens)
		}
	}
	return finish(f, fmt.Sprintf("Stack: %s/%s/%s", id.Environment, id.Team, id.Stack)), nil
}

// RenderVersions pins Terraform and the aws provider.
func RenderVersions(terraformVersion, awsProviderVersion string) []byte {
	f := hclwrite.NewEmptyFile()
	tf := f.Body().AppendNewBlock("terraform", nil).Body()
	tf.SetAttributeValue("required_version", cty.StringVal(terraformVersion))
	tf.AppendNewline()
	rp := tf.AppendNewBlock("required_providers", nil).Body()
	rp.SetAttributeValue("aws", cty.ObjectVal(map[string]cty.Value{
		"source":  cty.StringVal("hashicorp/aws"),
		"version": cty.StringVal(awsProviderVersion),
	}))
	return finish(f)
}

// RequiredTags returns the platform tags applied to every resource (AGENTS.md rule 5).
func RequiredTags(spec StackSpec) map[string]string {
	tags := map[string]string{
		"iacp:environment": spec.Environment,
		"iacp:managed-by":  "iacp",
		"iacp:stack":       spec.Stack,
		"iacp:team":        spec.Team,
	}
	if spec.CostCenter != "" {
		tags["cost-center"] = spec.CostCenter
	}
	return tags
}

// RenderProviders renders the aws provider with region, strategy attributes,
// optional endpoints and the required default tags.
func RenderProviders(spec StackSpec, pc ProviderConfig) ([]byte, error) {
	f := hclwrite.NewEmptyFile()
	p := f.Body().AppendNewBlock("provider", []string{"aws"}).Body()
	p.SetAttributeValue("region", cty.StringVal(spec.Region))
	for _, a := range pc.Attrs {
		if err := setAttr(p, a); err != nil {
			return nil, err
		}
	}
	if len(pc.Endpoints) > 0 {
		p.AppendNewline()
		eps := p.AppendNewBlock("endpoints", nil).Body()
		for _, svc := range fp.SortedKeys(pc.Endpoints) {
			eps.SetAttributeValue(svc, cty.StringVal(pc.Endpoints[svc]))
		}
	}
	p.AppendNewline()
	dt := p.AppendNewBlock("default_tags", nil).Body()
	dt.SetAttributeRaw("tags", stringMap(RequiredTags(spec)))
	return finish(f), nil
}

// RenderBackend renders terraform { backend "<type>" { ... } }.
func RenderBackend(blockType string, attrs []Attr) ([]byte, error) {
	f := hclwrite.NewEmptyFile()
	b := f.Body().AppendNewBlock("terraform", nil).Body().AppendNewBlock("backend", []string{blockType}).Body()
	for _, a := range attrs {
		if err := setAttr(b, a); err != nil {
			return nil, err
		}
	}
	return finish(f), nil
}

// RenderVariables renders the standard-structure variables.tf. Stack roots take no input
// variables (values are rendered into main.tf); cross-stack dependency variables
// (iacp.deps.auto.tfvars.json) will be declared here when WBS 9.1 lands.
func RenderVariables() []byte {
	return finish(hclwrite.NewEmptyFile(), "No input variables: inputs are rendered into main.tf.")
}

// RenderOutputs exposes each resource's declared outputs as one object output.
func RenderOutputs(res []decodedResource) []byte {
	f := hclwrite.NewEmptyFile()
	body := f.Body()
	first := true
	for _, r := range res {
		if len(r.Outputs) == 0 {
			continue
		}
		if !first {
			body.AppendNewline()
		}
		first = false
		outs := slices.Clone(r.Outputs)
		slices.SortFunc(outs, func(a, b catalog.Output) int { return strings.Compare(a.Name, b.Name) })
		attrs := fp.Map(outs, func(o catalog.Output) hclwrite.ObjectAttrTokens {
			return hclwrite.ObjectAttrTokens{Name: hclwrite.TokensForIdentifier(o.Name), Value: moduleRef(r.Name, o.Name)}
		})
		blk := body.AppendNewBlock("output", []string{r.Name}).Body()
		blk.SetAttributeValue("description", cty.StringVal(fmt.Sprintf("Outputs of %s (%s)", r.Name, catalog.GitTag(r.Module, r.Version))))
		blk.SetAttributeRaw("value", hclwrite.TokensForObject(attrs))
		if slices.ContainsFunc(outs, func(o catalog.Output) bool { return o.Sensitive }) {
			blk.SetAttributeValue("sensitive", cty.True)
		}
	}
	return finish(f)
}

type moduleRefJSON struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type stackJSON struct {
	Backend      string                   `json:"backend"`
	DependsOn    []string                 `json:"depends_on"`
	Environment  string                   `json:"environment"`
	FencingToken int64                    `json:"fencing_token"`
	Lifecycle    string                   `json:"lifecycle"`
	Modules      map[string]moduleRefJSON `json:"modules"`
	Region       string                   `json:"region"`
	RequestID    string                   `json:"request_id"`
	Stack        string                   `json:"stack"`
	Team         string                   `json:"team"`
}

// RenderStackJSON renders the platform metadata file (not read by Terraform).
// Field order is alphabetical and map keys are sorted by encoding/json.
func RenderStackJSON(spec StackSpec, res []decodedResource, backend string) ([]byte, error) {
	mods := make(map[string]moduleRefJSON, len(res))
	for _, r := range res {
		mods[r.Name] = moduleRefJSON{Ref: catalog.GitTag(r.Module, r.Version), SHA: r.GitSHA}
	}
	deps := slices.Clone(spec.DependsOn)
	slices.Sort(deps)
	if deps == nil {
		deps = []string{}
	}
	b, err := json.MarshalIndent(stackJSON{
		Backend: backend, DependsOn: deps, Environment: spec.Environment, FencingToken: spec.FencingToken,
		Lifecycle: spec.lifecycleOf(), Modules: mods, Region: spec.Region, RequestID: spec.RequestID, Stack: spec.Stack, Team: spec.Team,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", FileStackJSON, err)
	}
	return append(b, '\n'), nil
}
