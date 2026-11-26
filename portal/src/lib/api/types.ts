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

export interface StackRef {
  id: string;
  path: string;
  status: string;
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
  };
  created_by: string;
  created_at: string;
  updated_at: string;
  completed_at: string | null;
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
