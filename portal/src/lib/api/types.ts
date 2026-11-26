// Wire types for the control-plane REST API (backend/internal/httpapi, docs/03 §6).
// Field names match the JSON exactly; the portal never renames them in transit.

/** JSON Schema document as stored in module_versions.inputs_schema (draft 2020-12). */
export type JsonSchema = { readonly [key: string]: unknown };

export type CatalogStatus = "active" | "deprecated" | "yanked";

export interface ModuleOutput {
  name: string;
  description?: string;
  sensitive: boolean;
}

/** GET /v1/catalog/modules/{name}/versions/{version} */
export interface ModuleVersion {
  module: string;
  version: string;
  git_tag: string;
  git_sha: string;
  status: CatalogStatus;
  source: string;
  tf_version_range: string;
  aws_provider_range: string;
  inputs_schema: JsonSchema;
  outputs: ModuleOutput[];
  cost_hints: unknown;
  released_at: string;
}

/** An entry of GET /v1/catalog/modules; GET /v1/catalog/modules/{name} adds `latest`. */
export interface ModuleSummary {
  name: string;
  display_name: string;
  description: string;
  category: string;
  owners: string[];
  repo_path: string;
  status: CatalogStatus;
  latest_version: string | null;
  latest?: ModuleVersion;
}

/** An entry of GET /v1/catalog/modules/{name}/versions (SemVer descending). */
export interface VersionSummary {
  version: string;
  git_tag: string;
  status: CatalogStatus;
  deprecation_note?: string;
  released_at: string;
}

export type UpgradeChange = "none" | "patch" | "minor" | "major";

/** A step of GET /v1/catalog/modules/{name}/upgrade-path: one released version, oldest first. */
export interface UpgradeStep {
  version: string;
  git_tag: string;
  status: CatalogStatus;
  deprecation_note?: string;
  released_at: string;
  /** SemVer class of the move from the previous step (or from the pinned version). */
  change: UpgradeChange;
  breaking: boolean;
  /** The version's CHANGELOG section. */
  changelog_md: string;
  /** The version's migration notes ("### Migration" subsection or module.yaml upgrade.notes). */
  upgrade_notes: string;
}

/** GET /v1/catalog/modules/{name}/upgrade-path?from=X.Y.Z (backend httpapi upgradePathDTO). */
export interface UpgradePath {
  module: string;
  from: string;
  to: string;
  up_to_date: boolean;
  change: UpgradeChange;
  majors_crossed: number;
  /** The newest version one upgrade request may pin (one major at a time); null when up to date. */
  next: string | null;
  targets: { patch: string | null; minor: string | null; major: string | null };
  steps: UpgradeStep[];
}

export interface ModuleFilter {
  category?: string;
  status?: CatalogStatus;
}

export type RequestKind = "create" | "update" | "upgrade" | "destroy" | "reconcile" | "rollback";

export type RequestStatus =
  | "draft"
  | "pr_open"
  | "validating"
  | "plan_ready"
  | "approved"
  | "applying"
  | "applied"
  | "failed"
  | "rejected"
  | "cancelled";

export interface ResourceInput {
  name: string;
  module: string;
  version: string;
  inputs: Record<string, unknown>;
}

/** Body of POST /v1/requests (backend requests.CreateInput). */
export interface CreateRequestInput {
  kind: RequestKind;
  team: string;
  environment: string;
  stack: string;
  region?: string;
  ephemeral?: { ttl_hours: number };
  justification?: string;
  resources: ResourceInput[];
}

/** Body of POST /v1/requests with kind "update": the stack is named by id, resources replace its spec. */
export interface UpdateRequestInput {
  kind: "update";
  stack_id: string;
  justification?: string;
  /** Acknowledges that resources of the stack's current spec missing from `resources` are destroyed. */
  allow_removals?: boolean;
  resources: ResourceInput[];
}

/** Body of POST /v1/requests with kind "destroy": no resources; the name and a reason are required. */
export interface DestroyRequestInput {
  kind: "destroy";
  stack_id: string;
  confirm_stack_name: string;
  justification: string;
}

/** Body of POST /v1/requests with kind "upgrade": resources keep their inputs and move to to_version. */
export interface UpgradeRequestInput {
  kind: "upgrade";
  stack_id: string;
  justification?: string;
  /** Required when a move crosses a major: the caller read the migration notes. */
  acknowledge_breaking?: boolean;
  upgrades: { resource: string; to_version: string }[];
}

/** Everything POST /v1/requests accepts from the portal. */
export type RequestInput = CreateRequestInput | UpdateRequestInput | UpgradeRequestInput | DestroyRequestInput;

export type StackStatus = "provisioning" | "active" | "drifted" | "updating" | "destroying" | "destroyed" | "failed";

/** The most recent request against a stack (backend httpapi latestRequestDTO). */
export interface LatestRequest {
  id: string;
  short_id: string;
  kind: RequestKind;
  status: RequestStatus;
  created_at: string;
}

/** A stack as returned by GET /v1/stacks and GET /v1/stacks/{id} (backend httpapi stackDTO). */
export interface StackSummary {
  id: string;
  team: string;
  environment: string;
  name: string;
  path: string;
  region: string;
  status: StackStatus;
  is_ephemeral: boolean;
  expires_at: string | null;
  /** Non-sensitive outputs of the last apply, name to value, exactly as stored (redactions stay). */
  outputs: Record<string, unknown>;
  last_applied_sha?: string;
  latest_request: LatestRequest | null;
  created_at: string;
  updated_at: string;
  links: { self: string; latest_request: string | null };
}

export interface StackFilter {
  team?: string;
  env?: string;
  status?: StackStatus;
  limit?: number;
  cursor?: string;
}

export interface StackRef {
  id: string;
  path: string;
  status: string;
}

export interface PullRequestLink {
  branch: string;
  pr_number: number;
  pr_url: string;
  head_sha?: string;
}

/** A request as returned by POST, GET and list (backend httpapi requestDTO). */
export interface RequestSummary {
  id: string;
  short_id: string;
  kind: RequestKind;
  status: RequestStatus;
  stack: StackRef;
  queued_behind: string | null;
  justification?: string;
  /** Normalised StackSpec; only present on GET /v1/requests/{id}. */
  spec?: {
    team: string;
    environment: string;
    stack: string;
    region: string;
    ephemeral?: { ttl_hours: number };
    resources: ResourceInput[];
    depends_on: string[];
    /** What an upgrade request moved. */
    upgrades?: { resource: string; module: string; from_version: string; to_version: string; change: UpgradeChange }[];
  };
  created_by: string;
  created_at: string;
  updated_at: string;
  completed_at: string | null;
  /** The branch and pull request that carry the request's files; null until the PR is opened. */
  github?: PullRequestLink | null;
  /** Why the request failed, when it did (a short machine-readable reason). */
  failure_reason?: string;
  links: { self: string; events: string };
  allowed_events: string[];
}

export interface RequestFilter {
  team?: string;
  status?: RequestStatus;
  kind?: RequestKind;
  mine?: boolean;
  limit?: number;
  cursor?: string;
}

/** Collection envelope: {"data": [...], "next_cursor": "..." | null}. */
export interface Page<T> {
  data: T[];
  next_cursor: string | null;
}

export interface CreateRequestResult {
  request: RequestSummary;
  /** True when the backend answered from an earlier call with the same Idempotency-Key. */
  replayed: boolean;
}
