import type { Metadata } from "next";
import Link from "next/link";

import { ErrorPanel } from "@/components/ErrorPanel";
import { StatusBadge } from "@/components/StatusTimeline";
import { serverApi } from "@/lib/api/factory";
import { attempt } from "@/lib/api/result";
import { formatDateTime, requestStatusLabel } from "@/lib/format";

export const dynamic = "force-dynamic";
export const metadata: Metadata = { title: "Requests" };

const PAGE_SIZE = 25;

type Props = { searchParams: Promise<{ mine?: string; team?: string; cursor?: string }> };

/** listHref builds a /requests link for a filter state. Pure. */
function listHref(q: { mine?: boolean; team?: string; cursor?: string }): string {
  const p = new URLSearchParams();
  if (q.mine) p.set("mine", "true");
  if (q.team) p.set("team", q.team);
  if (q.cursor) p.set("cursor", q.cursor);
  const s = p.toString();
  return s ? `/requests?${s}` : "/requests";
}

export default async function RequestsPage({ searchParams }: Props) {
  const sp = await searchParams;
  const mine = sp.mine === "true";
  const team = sp.team?.trim() || undefined;
  const result = await attempt(() =>
    serverApi().listRequests({ mine, team, cursor: sp.cursor, limit: PAGE_SIZE }),
  );

  return (
    <section aria-labelledby="requests-title" className="space-y-6">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 id="requests-title" className="text-2xl font-semibold tracking-tight">
            Requests
          </h1>
          <p className="mt-1 text-slate-600">Newest first.</p>
        </div>
        <nav aria-label="Filter requests">
          <ul className="flex gap-2 text-sm">
            {[
              { label: "All", active: !mine, href: listHref({ team }) },
              { label: "Mine", active: mine, href: listHref({ mine: true, team }) },
            ].map((f) => (
              <li key={f.label}>
                <Link
                  href={f.href}
                  aria-current={f.active ? "page" : undefined}
                  className={`rounded-full px-3 py-1 ring-1 ring-inset ${
                    f.active ? "bg-slate-900 text-white ring-slate-900" : "bg-white ring-slate-300 hover:bg-slate-100"
                  }`}
                >
                  {f.label}
                </Link>
              </li>
            ))}
          </ul>
        </nav>
      </div>
      {team && (
        <p className="text-sm text-slate-700">
          Team <span className="font-mono">{team}</span> ·{" "}
          <Link href={listHref({ mine })} className="text-indigo-700 hover:underline">
            clear
          </Link>
        </p>
      )}

      {!result.ok ? (
        <ErrorPanel error={result.error} title="Could not load requests" />
      ) : result.data.data.length === 0 ? (
        <div className="rounded-lg border border-dashed border-slate-300 bg-white p-6 text-sm text-slate-700">
          No requests yet.{" "}
          <Link href="/catalog" className="text-indigo-700 hover:underline">
            Request a module from the catalog
          </Link>
          .
        </div>
      ) : (
        <>
          <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white">
            <table className="min-w-full text-left text-sm">
              <caption className="sr-only">Requests, newest first</caption>
              <thead className="bg-slate-50 text-xs uppercase tracking-wide text-slate-600">
                <tr>
                  <th scope="col" className="px-3 py-2">
                    Request
                  </th>
                  <th scope="col" className="px-3 py-2">
                    Status
                  </th>
                  <th scope="col" className="px-3 py-2">
                    Kind
                  </th>
                  <th scope="col" className="px-3 py-2">
                    Stack
                  </th>
                  <th scope="col" className="px-3 py-2">
                    Created
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {result.data.data.map((r) => (
                  <tr key={r.id}>
                    <th scope="row" className="px-3 py-2 font-mono text-xs font-medium">
                      <Link href={`/requests/${encodeURIComponent(r.id)}`} className="text-indigo-700 hover:underline">
                        {r.short_id}
                      </Link>
                    </th>
                    <td className="px-3 py-2">
                      <StatusBadge status={r.status} label={requestStatusLabel(r.status)} />
                      {r.queued_behind && <span className="ml-2 text-xs text-slate-500">queued</span>}
                    </td>
                    <td className="px-3 py-2">{r.kind}</td>
                    <td className="px-3 py-2 font-mono text-xs">{r.stack.path}</td>
                    <td className="whitespace-nowrap px-3 py-2 text-slate-700">{formatDateTime(r.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {result.data.next_cursor && (
            <Link
              href={listHref({ mine, team, cursor: result.data.next_cursor })}
              className="inline-block rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm hover:bg-slate-100"
            >
              Older requests
            </Link>
          )}
        </>
      )}
    </section>
  );
}
