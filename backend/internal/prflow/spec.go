// Package prflow turns a draft request that holds its stack lock into a branch and a
// pull request: it renders the stack with the generator, publishes it through a
// publish.Publisher, and records the result on the request (pr_opened).
//
// The flow is two-step so a GitHub failure never leaves the database inconsistent: the
// request and its outbox job commit first (requests.Service.Create), the worker then
// calls Service.Handle outside any transaction to write to the repository, and a second
// transaction records the PR (or the failure). Every publisher call is idempotent, so a
// retried job converges on the same branch and PR.
package prflow

import (
	"encoding/json"
	"fmt"

	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/catalog"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/fp"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/generator"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/publish"
	"github.com/bibekgupta3333/self-service-iac-platform/backend/internal/requests"
)

// Default version constraints rendered into versions.tf (the smoke stack uses the same).
const (
	DefaultTerraformVersion   = "~> 1.13"
	DefaultAWSProviderVersion = "~> 5.80"
)

// VersionKey indexes resolved module versions. Pure.
func VersionKey(module, version string) string { return module + "@" + version }

// DecodeSpec parses the normalised spec stored on a request. Pure.
func DecodeSpec(r *requests.Request) (requests.NormalizedSpec, error) {
	var spec requests.NormalizedSpec
	if err := json.Unmarshal(r.Spec, &spec); err != nil {
		return requests.NormalizedSpec{}, fmt.Errorf("decode spec of %s: %w", r.ShortID, err)
	}
	return spec, nil
}

// BuildStackSpec maps a request, its normalised spec and the resolved module versions to
// the generator input. Pure.
func BuildStackSpec(r *requests.Request, spec requests.NormalizedSpec, versions map[string]catalog.ModuleVersion,
	costCenter, terraformVersion, awsProviderVersion string) (generator.StackSpec, error) {
	if r.FencingToken == nil {
		return generator.StackSpec{}, fmt.Errorf("request %s holds no stack lock", r.ShortID)
	}
	res := make([]generator.Resource, 0, len(spec.Resources))
	for _, in := range spec.Resources {
		v, ok := versions[VersionKey(in.Module, in.Version)]
		if !ok {
			return generator.StackSpec{}, fmt.Errorf("resource %s: %s@%s was not resolved", in.Name, in.Module, in.Version)
		}
		res = append(res, generator.Resource{Name: in.Name, Module: in.Module, Version: v.Version, GitSHA: v.GitSHA,
			Inputs: in.Inputs, Outputs: v.Outputs})
	}
	lifecycle := generator.LifecycleActive
	if r.Kind == requests.KindDestroy {
		// The root is emptied and marked, so the plan shows every resource being destroyed.
		lifecycle, res = generator.LifecycleDestroy, nil
	}
	return generator.StackSpec{
		Lifecycle:   lifecycle,
		Environment: spec.Environment, Team: spec.Team, Stack: spec.Stack, Region: spec.Region, CostCenter: costCenter,
		RequestID: r.ShortID, FencingToken: *r.FencingToken, TerraformVersion: terraformVersion,
		AWSProviderVersion: awsProviderVersion, Resources: res, DependsOn: spec.DependsOn,
	}, nil
}

// PRContextFor builds the PR rendering input for a request. Pure.
func PRContextFor(r *requests.Request, spec requests.NormalizedSpec, portalURL string) publish.PRContext {
	var token int64
	if r.FencingToken != nil {
		token = *r.FencingToken
	}
	return publish.PRContext{
		RequestID: r.ID.String(), ShortID: r.ShortID, Kind: string(r.Kind), Environment: spec.Environment,
		Team: spec.Team, Stack: spec.Stack, StackPath: r.Stack.Path, Region: spec.Region,
		Justification: r.Justification, FencingToken: token, RequestURL: portalURL + "/requests/" + r.ID.String(),
		RollbackOf: spec.RollbackOf,
		Resources: fp.Map(spec.Resources, func(in requests.ResourceInput) publish.PRResource {
			return publish.PRResource{Name: in.Name, Module: in.Module, Version: in.Version}
		}),
	}
}
