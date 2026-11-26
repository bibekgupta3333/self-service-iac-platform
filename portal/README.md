# portal/

Next.js developer portal: browse the module catalog, request a stack from a module version's
input schema, and track requests. WBS §7 (M4): 7.1 scaffold, 7.2 catalog, 7.3 request form,
7.5 requests list and detail.

The portal only talks to the control-plane API ([backend/](../backend/README.md),
[docs/03 §6](../docs/03-data-model-and-api.md#6-rest-api-reference)). It never runs Terraform
and holds no cloud credentials.

## Run locally

Node is pinned in `.nvmrc` (24.18.0; `engines` requires >= 24.15). Dependencies are exact
versions from `package-lock.json`.

```bash
make up                 # PostgreSQL + LocalStack
make api                # API on 127.0.0.1:8080 in dev auth mode
make portal-dev         # portal on http://localhost:3000 (npm ci on first run)
make portal-test        # lint + typecheck + unit tests
```

An empty catalog means no module version is indexed yet. Index the repo's modules against the
local API (see [tools/README.md](../tools/README.md)):

```bash
for m in s3 sqs lambda; do
  IACP_API_URL=http://127.0.0.1:8080 IACP_INTERNAL_TOKEN=local-dev-internal-token-0123456789abcdef \
    .venv/bin/python -m tools.module_release index "$m" --commit-sha "$(git rev-parse HEAD)"
done
```

Creating a request needs a team where the dev user has the `developer` role; the SQL is in
[backend/README.md](../backend/README.md#run).

| Variable | Side | Default | Purpose |
|----------|------|---------|---------|
| `IACP_API_URL` | server | `http://127.0.0.1:8080` | API origin for server components and the `/api/iacp` proxy |
| `NEXT_PUBLIC_IACP_API_URL` | browser (build time) | unset | call the API directly from the browser; needs CORS on the API, which it does not send yet |
| `NEXT_PUBLIC_IACP_AUTH_MODE` | both (build time) | `dev` | auth provider; only `dev` exists |
| `NEXT_PUBLIC_IACP_DEV_USER` | both (build time) | `local-dev` | display label; set it to the API's `IACP_DEV_USER` |

Do not commit `.env*` files; pass variables on the command line or through your shell.

## Authentication (dev mode)

The backend picks its user authenticator with `IACP_AUTH_MODE`. The only mode so far is `dev`
(`httpapi.DevAuthenticator`): the API ignores the request and treats every `/v1` call as one
local user, upserted at startup from `IACP_DEV_USER`. There is no header, cookie or token, and
no `GET /v1/me` yet.

The portal does exactly that through `AuthProvider` (`src/lib/auth/provider.ts`):
`DevAuthProvider.requestHeaders()` returns nothing and `viewer()` returns the configured login
for the header badge. `createAuthProvider` refuses any other mode rather than falling back.
GitHub OAuth (WBS 4.4) replaces it with a provider that relies on the `iacp_session` cookie
(`credentials: "include"`), adds `X-CSRF-Token` on mutations and reads the user from `/v1/me`.
`ApiClient` and the pages only see the interface. The proxy already forwards `Cookie`,
`Authorization` and `X-CSRF-Token` for that.

## How it talks to the API

- **Server components** (catalog, module, request list and detail) call the API directly with
  `serverApi()` and `cache: "no-store"`; every page is dynamic.
- **Client components** (the request form) use `browserApi()`, which goes through the
  same-origin route `src/app/api/iacp/[...path]/route.ts` because the API sends no CORS
  headers. The proxy forwards only user routes under `/v1` (never `/v1/internal/*` or
  `/v1/runs`), validates and re-encodes path segments, and allow-lists request and response
  headers.
- **Errors**: non-2xx responses become `ApiError`, built by the pure `parseProblem` from the
  RFC 9457 body (`code`, `detail`, `request_id`, `errors[].pointer`). Transport failures are
  `network_error`. Pages show the request id for support.

## Request form

The form is generated from the selected version's `inputs_schema` (JSON Schema 2020-12 from
`module.yaml`), plus team, environment, stack name, resource name and justification.

- **Renderer: a small typed renderer, not `@rjsf/core`.** rjsf 6 does support React 19, so
  compatibility was not the reason. The reasons were:
  - The portal needs a pure schema-to-field model anyway for the 7.2 inputs table, and the same
    model drives the form.
  - Backend errors arrive as pointers (`/resources/0/inputs/<name>`) and map one-to-one onto
    that model.
  - The schemas are flat: scalars, enums, string lists and string maps, with `x-iacp-group`,
    `x-iacp-immutable` and `x-iacp-ui` hints. rjsf would also need `Ajv2020` plus custom-keyword
    setup and adds a large bundle for a client check the backend repeats anyway.
  - Anything richer (nested objects, arrays of objects, `if`/`then`) gets a JSON editor, and
    the backend validates the full schema.
- Only required values and values that differ from the module default are sent. Clearing a
  nullable field whose default is not null sends `null`.
- `POST /v1/requests` carries an `Idempotency-Key`: one UUID per form session, created on the
  first submit and reused for retries, so a request that reached the backend is never created
  twice.
- Backend validation errors are mapped onto the field their pointer names. The rest (module
  version, resource list) go to an error summary, which takes focus and links to each field.
- **Preview** shows the exact HTTP request. A preview of the generated Terraform files needs
  `POST /v1/requests/{id}/preview` (docs/03 §6.4), which the backend does not have yet:
  follow-up.

## Requests

`/requests` lists requests newest first (All/Mine, team filter, keyset paging).
`/requests/{id}` places the current status on the request state machine and shows the
normalised spec. The API returns only the current status, so a failed, rejected or cancelled
request shows its outcome without the steps it reached. While a request is not terminal the
page refreshes every 5 seconds; the SSE stream (`/v1/requests/{id}/events`) replaces polling
in WBS 7.4, together with PR, plan, cost and run links. A stacks list (rest of 7.5) needs
`GET /v1/stacks`, which is not built yet.

## Layout and style

```
src/app/                  routes (App Router); api/iacp/ is the proxy
src/components/           presentational components; request/ holds the form
src/lib/api/              ApiClient (I/O), types, errors, config, proxy rules, factory
src/lib/auth/             AuthProvider + DevAuthProvider
src/lib/schema/           pure schema -> field model, form values <-> inputs, SchemaAdapter
src/lib/request/          pure stack-field checks, payload builder, pointer -> field mapping
src/lib/format.ts         pure dates, labels, tones, status timeline
```

About 70% object-oriented, 30% functional, as in the backend. `ApiClient`, the auth providers
and `SchemaAdapter` are classes with their dependencies injected. Schema translation, value
parsing, error mapping and formatting are pure functions. I/O happens only in `ApiClient`, the
proxy route and the server-side factory. Tests (Vitest + Testing Library) cover the pure
functions, the client with a fake `fetch`, and the form, inputs table, timeline and nav
components.

Accessibility: a skip link, semantic landmarks and tables, a label for every control,
`aria-invalid`/`aria-describedby` for errors, an error summary that takes focus,
`aria-current` for navigation and the current timeline step, and one visible focus style.
