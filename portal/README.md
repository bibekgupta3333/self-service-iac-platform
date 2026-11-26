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
| `NEXT_PUBLIC_IACP_AUTH_MODE` | both (build time) | `dev` | auth provider: `dev` or `github` (must match the backend's `IACP_AUTH_MODE`) |
| `NEXT_PUBLIC_IACP_DEV_USER` | both (build time) | `local-dev` | display label; set it to the API's `IACP_DEV_USER` |
| `IACP_PORTAL_ORIGIN` | server | unset | public origin of the portal behind a proxy that rewrites `Host`, accepted by the proxy's same-origin check |

Do not commit `.env*` files; pass variables on the command line or through your shell.

## Authentication

The backend picks its user authenticator with `IACP_AUTH_MODE`, and the portal's
`NEXT_PUBLIC_IACP_AUTH_MODE` must match. Pages and `ApiClient` only see `AuthProvider`
(`src/lib/auth/provider.ts`); `createAuthProvider` refuses unknown modes rather than falling back.

**`dev`** (`httpapi.DevAuthenticator`): the API ignores the request and treats every `/v1` call
as one local user, upserted at startup from `IACP_DEV_USER`. There is no header, cookie or
token. `DevAuthProvider` returns no headers and labels the badge. Nothing about dev mode changed
with OAuth.

**`github`** (WBS 4.4): GitHub OAuth. `GithubAuthProvider` uses `credentials: "include"`, adds
the per-session `X-CSRF-Token` to unsafe methods (read once from `GET /v1/me`), and reads the
viewer from `/v1/me`. On the server, `src/lib/api/server.ts` forwards the incoming `Cookie`
header to the API (via `next/headers`), so pages render with the signed-in user's rights.
Without a session the header shows "Sign in with GitHub" and a 401 error panel links to it.
"Sign out" posts to `/v1/auth/logout` with the token and reloads.

**Where the cookie lives.** The login runs through the same-origin proxy, so the session cookie
belongs to the portal's origin and the browser sends it back to the proxy, which forwards it.
The proxy used to drop `Set-Cookie`; it now passes it through in one controlled place:
`GET /api/iacp/v1/auth/login`, `GET .../auth/callback` and `POST .../auth/logout` only, and only
cookies named `iacp_session` or `iacp_oauth_state` (over https the backend prefixes them with
`__Host-`, and those names are allowed too) without a `Domain` attribute (`filterSetCookies`).
On those paths a `Location` header is accepted only if it is exactly GitHub's authorize URL
(`https://github.com/login/oauth/authorize?...`) or a path on the portal that does not start
with `//`, contain a backslash or a control character (`rewriteAuthLocation`). Every other
absolute URL is dropped, so a compromised or misconfigured backend cannot make the portal
redirect a browser to another site, and the backend's `return_to` allow-list
(`IACP_AUTH_REDIRECT_ALLOWLIST`) can only ever yield portal paths here. The browser would
follow an absolute portal URL too, but the proxy drops it: configure `return_to` as a path.
Logout is the one POST that needs no `X-CSRF-Token` at the proxy (a browser with an expired
session has no token to send); the backend requires it whenever the session is live. Register the GitHub OAuth app's
callback as `https://<portal>/api/iacp/v1/auth/callback` (locally
`http://localhost:3000/api/iacp/v1/auth/callback`) and set the backend's
`IACP_GITHUB_CALLBACK_URL` to the same value; docs/07 section 7 lists the full app settings.
Do not point the callback at the backend origin: the cookie would then belong to the wrong
origin and the proxy could not forward it.

Local run: start the API with `IACP_AUTH_MODE=github` and the `IACP_GITHUB_*` variables, and the
portal with `NEXT_PUBLIC_IACP_AUTH_MODE=github make portal-dev`.

## How it talks to the API

- **Server components** (catalog, module, request list and detail) call the API directly with
  `serverApi()` (`src/lib/api/server.ts`) and `cache: "no-store"`; every page is dynamic.
- **Client components** (the request form) use `browserApi()` (`src/lib/api/factory.ts`), which goes through the
  same-origin route `src/app/api/iacp/[...path]/route.ts` because the API sends no CORS
  headers. Its rules are pure functions in `src/lib/api/proxy.ts`:
  - **Allow-list, not deny-list.** Only the first segments the portal calls (`catalog`,
    `requests`) are forwarded, matched exactly and case-sensitively on the segments Next.js
    has decoded once. Every segment must be unreserved URL characters other than `.` and `..`,
    so `Internal`, `RUNS`, `%69nternal`, encoded slashes and traversal are all refused (404).
    Machine routes (`/v1/internal/*`, `/v1/runs`) and any route added later stay unreachable
    until they are added on purpose. The login flow adds `me` and exactly three `auth` paths
    (`login`, `callback`, `logout`).
  - **Methods.** GET, and HEAD (answered from an upstream GET with no body). POST only on
    `/v1/requests` and `/v1/auth/logout`. Anything else gets 405 with `Allow`.
  - **Bodies** over 1 MiB (the backend's own cap) get 413, by `Content-Length` or while
    streaming.
  - **CSRF.** A POST must carry `Sec-Fetch-Site: same-origin`, or, without that header, an
    `Origin` equal to the portal's (request URL, `Host`, or `IACP_PORTAL_ORIGIN`); otherwise
    403. Requests with neither header come from non-browser clients and carry no ambient
    browser credentials, so they pass only when they send no `Cookie`. A POST with a `Cookie`
    and neither header is refused, and with a `Cookie` the `X-CSRF-Token` header must be
    present (the backend verifies its value). Dev mode sends no cookie and is unaffected.
  - **Headers.** Request and response headers are allow-listed (`Server` from the API is
    dropped, and `Set-Cookie` passes only on the login-flow routes, described above). `Location: /v1/...` is rewritten to `/api/iacp/v1/...`.
- **Errors**: non-2xx responses become `ApiError`, built by the pure `parseProblem` from the
  RFC 9457 body (`code`, `detail`, `request_id`, `errors[].pointer`). Transport failures are
  `network_error`. Pages show the request id for support.

## Request form

The form is generated from the selected version's `inputs_schema` (JSON Schema 2020-12 from
`module.yaml`), plus team, environment, stack name, resource name and justification.

- **Renderer: a small typed renderer, not `@rjsf/core`** ([ADR-0011](../docs/adr/0011-portal-form-renderer.md)).
  rjsf 6 does support React 19, so compatibility was not the reason. The reasons were:
  - The portal needs a pure schema-to-field model anyway for the 7.2 inputs table, and the same
    model drives the form.
  - Backend errors arrive as pointers (`/resources/0/inputs/<name>`) and map one-to-one onto
    that model.
  - The schemas are flat: scalars, enums, string lists and string maps, with `x-iacp-group`,
    `x-iacp-immutable` and `x-iacp-ui` hints. rjsf would also need `Ajv2020` plus custom-keyword
    setup and adds a large bundle for a client check the backend repeats anyway.
  - Anything richer (nested objects, arrays of objects, `if`/`then`) gets a JSON editor, and
    the backend validates the full schema.
- Only required values and values that differ from the module default are sent. What an
  emptied optional control sends (`clearBehaviour`, shown in each field's hint):
  - `null` when the field is nullable and its default is not;
  - the explicit empty value (`""`, `[]`, `{}`) when the default is non-empty and the schema
    allows it (`minLength`, `pattern`, `minItems`, `minProperties`);
  - otherwise nothing, so the module default applies. Numbers, booleans, enums and JSON fields
    have no empty value other than `null`, so a non-nullable one cannot be "cleared": leaving
    it empty keeps the default.
- Client checks treat `exclusiveMinimum`/`exclusiveMaximum` as exclusive.
- **Idempotency-Key** (`src/lib/request/idempotency.ts`). The key is bound to the payload it
  was sent with:
  - Resubmitting unchanged values reuses it, so a request that reached the backend after a
    timeout or 5xx is replayed rather than created twice.
  - Changing any value mints a new key, so the backend never answers
    `422 idempotency_key_reuse` for an edit.
  - After an ambiguous failure (network error or 5xx), the form warns that the request may
    exist and links to `/requests?mine=true`.
  - If `idempotency_key_reuse` does come back, the form explains it, links there too, and
    starts over with a new key.
- Backend validation errors are mapped onto the field their pointer names, including missing
  required inputs, which the backend reports at `/resources/0/inputs` with the names in the
  message. The rest (module version, resource list) go to an error summary, which takes focus
  and links to each field.
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

## Security headers

Every response carries `Content-Security-Policy` (`src/lib/security/csp.ts`):
- every source is `'self'`, plus the API origin when `NEXT_PUBLIC_IACP_API_URL` is set;
- `object-src 'none'`, `frame-ancestors 'none'` and `form-action 'self'`.

It also sends `X-Content-Type-Options`, `X-Frame-Options` and `Referrer-Policy`.

Next.js still needs `'unsafe-inline'` for its bootstrap scripts and styles. Moving to
per-request nonces in middleware is a follow-up. `next dev` additionally allows
`'unsafe-eval'` and `ws:` for React Refresh.

## Layout and style

```
src/app/                  routes (App Router); api/iacp/ is the proxy
src/components/           presentational components; request/ holds the form
src/lib/api/              ApiClient (I/O), types, errors, config, proxy rules, factory
src/lib/auth/             AuthProvider, DevAuthProvider, GithubAuthProvider
src/lib/schema/           pure schema -> field model, form values <-> inputs, SchemaAdapter
src/lib/request/          pure stack-field checks, payload builder, pointer -> field mapping, Idempotency-Key rules
src/lib/format.ts         pure dates, labels, tones, categories, status timeline
src/lib/security/         pure Content-Security-Policy builder
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
