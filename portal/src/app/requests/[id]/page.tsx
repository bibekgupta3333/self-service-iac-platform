import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import type { ReactNode } from "react";

import { ErrorPanel } from "@/components/ErrorPanel";
import { RequestLive } from "@/components/RequestLive";
import { serverApi } from "@/lib/api/server";
import { attempt, isNotFound } from "@/lib/api/result";
import { formatDateTime } from "@/lib/format";

export const dynamic = "force-dynamic";

type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  return { title: `Request ${(await params).id.slice(0, 8)}` };
}

export default async function RequestPage({ params }: Props) {
  const { id } = await params;
  const result = await attempt(() => serverApi().getRequest(id));
  if (!result.ok && isNotFound(result.error)) notFound();
  if (!result.ok) return <ErrorPanel error={result.error} title="Could not load the request" />;
  const r = result.data;

  return (
    <article aria-labelledby="request-title" className="space-y-8">
      <header className="space-y-2">
        <p className="text-sm">
          <Link href="/requests" className="text-indigo-700 hover:underline">
            Requests
          </Link>{" "}
          <span aria-hidden="true">/</span> <span className="font-mono">{r.short_id}</span>
        </p>
        <div className="flex flex-wrap items-center gap-3">
          <h1 id="request-title" className="font-mono text-2xl font-semibold tracking-tight">
            {r.short_id}
          </h1>
        </div>
      </header>

      <RequestLive request={r} />

      <section aria-labelledby="details-title" className="space-y-3">
        <h2 id="details-title" className="text-base font-semibold">
          Details
        </h2>
        <dl className="grid gap-x-6 gap-y-3 rounded-lg border border-slate-200 bg-white p-5 text-sm sm:grid-cols-2">
          <Item label="Kind">{r.kind}</Item>
          <Item label="Stack">
            <Link href={`/stacks/${encodeURIComponent(r.stack.id)}`} className="font-mono text-xs text-indigo-700 hover:underline">
              {r.stack.path}
            </Link>{" "}
            ({r.stack.status})
          </Item>
          {r.spec && (
            <>
              <Item label="Team">
                <Link href={`/requests?team=${encodeURIComponent(r.spec.team)}`} className="text-indigo-700 hover:underline">
                  {r.spec.team}
                </Link>
              </Item>
              <Item label="Environment">
                {r.spec.environment} ({r.spec.region})
              </Item>
            </>
          )}
          <Item label="Created">{formatDateTime(r.created_at)}</Item>
          <Item label="Updated">{formatDateTime(r.updated_at)}</Item>
          {r.justification && <Item label="Justification">{r.justification}</Item>}
          <Item label="Request ID">
            <span className="font-mono text-xs">{r.id}</span>
          </Item>
        </dl>
      </section>

      {r.spec && (
        <section aria-labelledby="resources-title" className="space-y-3">
          <h2 id="resources-title" className="text-base font-semibold">
            Resources
          </h2>
          <ul className="space-y-3">
            {r.spec.resources.map((res) => (
              <li key={res.name} className="rounded-lg border border-slate-200 bg-white p-5">
                <p className="text-sm">
                  <span className="font-mono font-medium">{res.name}</span>{" "}
                  <Link
                    href={`/catalog/${encodeURIComponent(res.module)}?version=${encodeURIComponent(res.version)}`}
                    className="font-mono text-xs text-indigo-700 hover:underline"
                  >
                    {res.module}@{res.version}
                  </Link>
                </p>
                <pre className="mt-2 overflow-x-auto rounded-md bg-slate-50 p-3 text-xs">
                  <code>{JSON.stringify(res.inputs, null, 2)}</code>
                </pre>
              </li>
            ))}
          </ul>
        </section>
      )}

    </article>
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
