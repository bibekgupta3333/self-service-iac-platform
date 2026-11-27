import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import type { ReactNode } from "react";

import { Badge } from "@/components/Badge";
import { ErrorPanel } from "@/components/ErrorPanel";
import { StatusBadge } from "@/components/StatusTimeline";
import { DestroyStack } from "@/components/DestroyStack";
import { StackActionLink } from "@/components/StackActionLink";
import { UpgradeResource } from "@/components/UpgradeResource";
import { serverApi, serverAuth } from "@/lib/api/server";
import { attempt, isNotFound } from "@/lib/api/result";
import type { ResourceInput, StackSummary } from "@/lib/api/types";
import { formatDateTime, requestStatusLabel, shortSha } from "@/lib/format";
import { currentSpecRequest } from "@/lib/request/lifecycle";
import { planResourceUpgrade, upgradesAvailable } from "@/lib/request/upgrade";
import { outputRows, stackActions, stackHealth, stackStatusLabel } from "@/lib/stacks";

export const dynamic = "force-dynamic";

type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  return { title: `Stack ${(await params).id.slice(0, 8)}` };
}

export default async function StackPage({ params }: Props) {
  const { id } = await params;
  const result = await attempt(() => serverApi().getStack(id));
  if (!result.ok && isNotFound(result.error)) notFound();
  if (!result.ok) return <ErrorPanel error={result.error} title="Could not load the stack" />;
  const s = result.data;
  const health = stackHealth(s.status);
  const outputs = outputRows(s.outputs);
  const actions = stackActions(s, await serverAuth().capabilities());
  const upgrades = await resourceUpgrades(s);
  const available = upgradesAvailable(upgrades);

  return (
    <article aria-labelledby="stack-title" className="space-y-8">
      <header className="space-y-2">
        <p className="text-sm">
          <Link href="/stacks" className="text-indigo-700 hover:underline">
            Stacks
          </Link>{" "}
          <span aria-hidden="true">/</span> <span className="font-mono">{s.name}</span>
        </p>
        <div className="flex flex-wrap items-center gap-3">
          <h1 id="stack-title" className="font-mono text-2xl font-semibold tracking-tight">
            {s.name}
          </h1>
          <Badge tone={health.tone}>{stackStatusLabel(s.status)}</Badge>
        </div>
        <p className="font-mono text-xs text-slate-600">{s.path}</p>
      </header>

      <section aria-labelledby="actions-title" className="space-y-3">
        <h2 id="actions-title" className="text-base font-semibold">
          Actions
        </h2>
        <div className="flex flex-wrap items-start gap-4">
          <StackActionLink
            href={`/stacks/${encodeURIComponent(s.id)}/update`}
            label="Update"
            state={actions.update}
            describedBy="update-reason"
          />
          <DestroyStack
            stack={{ id: s.id, name: s.name, path: s.path, team: s.team, environment: s.environment }}
            state={actions.destroy}
          />
        </div>
        {!actions.update.enabled && (
          <p id="update-reason" className="text-sm text-slate-600">
            Update unavailable: {actions.update.reason}
          </p>
        )}
      </section>

      {upgrades.length > 0 && (
        <section aria-labelledby="versions-title" className="space-y-3">
          <div className="flex flex-wrap items-center gap-3">
            <h2 id="versions-title" className="text-base font-semibold">
              Module versions
            </h2>
            {available > 0 && (
              <Badge tone="info">{available === 1 ? "1 upgrade available" : `${available} upgrades available`}</Badge>
            )}
          </div>
          <p className="text-sm text-slate-600">
            Each resource pins an exact module version. An upgrade keeps the inputs, pins a newer version and opens a pull
            request; a major version is upgraded one at a time, after its migration notes.
          </p>
          <div className="space-y-3">
            {upgrades.map((u) => (
              <UpgradeResource
                key={u.resource}
                stack={{ id: s.id, name: s.name, environment: s.environment }}
                upgrade={u}
                state={actions.update}
              />
            ))}
          </div>
        </section>
      )}

      <section aria-labelledby="health-title" className="space-y-2 rounded-lg border border-slate-200 bg-white p-5">
        <h2 id="health-title" className="text-base font-semibold">
          Health
        </h2>
        <p className="text-sm">
          <span className="font-medium">{health.label}.</span> {health.description}
        </p>
      </section>

      <section aria-labelledby="latest-title" className="space-y-2">
        <h2 id="latest-title" className="text-base font-semibold">
          Latest request
        </h2>
        {s.latest_request ? (
          <p className="flex flex-wrap items-center gap-3 rounded-lg border border-slate-200 bg-white p-4 text-sm">
            <Link
              href={`/requests/${encodeURIComponent(s.latest_request.id)}`}
              className="font-mono text-indigo-700 hover:underline"
            >
              {s.latest_request.short_id}
            </Link>
            <span>{s.latest_request.kind}</span>
            <StatusBadge status={s.latest_request.status} label={requestStatusLabel(s.latest_request.status)} />
            <span className="text-slate-600">{formatDateTime(s.latest_request.created_at)}</span>
          </p>
        ) : (
          <p className="text-sm text-slate-600">No request is recorded for this stack.</p>
        )}
      </section>

      <section aria-labelledby="details-title" className="space-y-3">
        <h2 id="details-title" className="text-base font-semibold">
          Details
        </h2>
        <dl className="grid gap-x-6 gap-y-3 rounded-lg border border-slate-200 bg-white p-5 text-sm sm:grid-cols-2">
          <Item label="Team">
            <Link href={`/stacks?team=${encodeURIComponent(s.team)}`} className="text-indigo-700 hover:underline">
              {s.team}
            </Link>
          </Item>
          <Item label="Environment">{s.environment}</Item>
          <Item label="Region">{s.region}</Item>
          <Item label="Ephemeral">
            {s.is_ephemeral ? (s.expires_at ? `yes, destroyed after ${formatDateTime(s.expires_at)}` : "yes") : "no"}
          </Item>
          {s.last_applied_sha && (
            <Item label="Last applied commit">
              <span className="font-mono text-xs">{shortSha(s.last_applied_sha)}</span>
            </Item>
          )}
          <Item label="Created">{formatDateTime(s.created_at)}</Item>
          <Item label="Updated">{formatDateTime(s.updated_at)}</Item>
          <Item label="Stack ID">
            <span className="font-mono text-xs">{s.id}</span>
          </Item>
        </dl>
      </section>

      <section aria-labelledby="outputs-title" className="space-y-3">
        <h2 id="outputs-title" className="text-base font-semibold">
          Outputs
        </h2>
        {outputs.length === 0 ? (
          <p className="text-sm text-slate-600">No outputs recorded yet. They appear after the first successful apply.</p>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white">
            <table className="min-w-full text-left text-sm">
              <caption className="sr-only">Outputs of the last apply</caption>
              <thead className="bg-slate-50 text-xs uppercase tracking-wide text-slate-600">
                <tr>
                  <th scope="col" className="px-3 py-2">
                    Name
                  </th>
                  <th scope="col" className="px-3 py-2">
                    Value
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {outputs.map((o) => (
                  <tr key={o.name}>
                    <th scope="row" className="whitespace-nowrap px-3 py-2 font-mono text-xs font-medium">
                      {o.name}
                    </th>
                    <td className="break-all px-3 py-2 font-mono text-xs">{o.text}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <p className="text-xs text-slate-500">Shown exactly as the platform returns them; sensitive values are never included.</p>
      </section>
    </article>
  );
}

/**
 * resourceUpgrades loads the stack's current resources (the spec of the request that carries
 * it, as the update page does) and each resource's upgrade path. A path that cannot be loaded
 * is shown as unknown rather than failing the page.
 */
async function resourceUpgrades(stack: StackSummary) {
  const api = serverApi();
  let specRequestId = currentSpecRequest(stack, []);
  if (!specRequestId) {
    const recent = await attempt(() => api.listRequests({ team: stack.team, limit: 100 }));
    if (recent.ok) specRequestId = currentSpecRequest(stack, recent.data.data);
  }
  if (!specRequestId) return [];
  const spec = await attempt(() => api.getRequest(specRequestId));
  const resources: ResourceInput[] = spec.ok ? (spec.data.spec?.resources ?? []) : [];
  return Promise.all(
    resources.map(async (r) => {
      const path = await attempt(() => api.getUpgradePath(r.module, r.version));
      return planResourceUpgrade(r, path.ok ? path.data : undefined);
    }),
  );
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="font-medium text-slate-600">{label}</dt>
      <dd className="mt-0.5 text-slate-900">{children}</dd>
    </div>
  );
}
