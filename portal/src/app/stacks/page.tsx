import type { Metadata } from "next";
import Link from "next/link";

import { Badge } from "@/components/Badge";
import { ErrorPanel } from "@/components/ErrorPanel";
import { StatusBadge } from "@/components/StatusTimeline";
import { serverApi } from "@/lib/api/server";
import { attempt } from "@/lib/api/result";
import { formatDateTime, requestStatusLabel } from "@/lib/format";
import { ENVIRONMENTS } from "@/lib/request/form";
import { parseStackQuery, STACK_STATUSES, stackHealth, stacksHref, stackStatusLabel } from "@/lib/stacks";

export const dynamic = "force-dynamic";
export const metadata: Metadata = { title: "Stacks" };

const PAGE_SIZE = 25;

type Props = { searchParams: Promise<Record<string, string | string[] | undefined>> };

const FIELD =
  "mt-1 block rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm shadow-sm";

export default async function StacksPage({ searchParams }: Props) {
  const query = parseStackQuery(await searchParams);
  const result = await attempt(() => serverApi().listStacks({ ...query, limit: PAGE_SIZE }));
  const filtered = Boolean(query.team || query.env || query.status);

  return (
    <section aria-labelledby="stacks-title" className="space-y-6">
      <div>
        <h1 id="stacks-title" className="text-2xl font-semibold tracking-tight">
          Stacks
        </h1>
        <p className="mt-1 text-slate-600">The infrastructure your teams run, newest first.</p>
      </div>

      {/* A plain GET form: it works without scripts, and each filter change starts from page one. */}
      <form method="get" action="/stacks" aria-label="Filter stacks" className="flex flex-wrap items-end gap-4">
        <div>
          <label htmlFor="filter-team" className="text-sm font-medium text-slate-800">
            Team
          </label>
          <input
            id="filter-team"
            name="team"
            type="text"
            defaultValue={query.team ?? ""}
            autoComplete="off"
            spellCheck={false}
            placeholder="payments"
            className={FIELD}
          />
        </div>
        <div>
          <label htmlFor="filter-env" className="text-sm font-medium text-slate-800">
            Environment
          </label>
          <select id="filter-env" name="env" defaultValue={query.env ?? ""} className={FIELD}>
            <option value="">All</option>
            {ENVIRONMENTS.map((e) => (
              <option key={e} value={e}>
                {e}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label htmlFor="filter-status" className="text-sm font-medium text-slate-800">
            Status
          </label>
          <select id="filter-status" name="status" defaultValue={query.status ?? ""} className={FIELD}>
            <option value="">All</option>
            {STACK_STATUSES.map((s) => (
              <option key={s} value={s}>
                {stackStatusLabel(s)}
              </option>
            ))}
          </select>
        </div>
        <button type="submit" className="rounded-md bg-slate-900 px-4 py-1.5 text-sm font-medium text-white hover:bg-slate-700">
          Apply filters
        </button>
        {filtered && (
          <Link href="/stacks" className="py-1.5 text-sm text-indigo-700 hover:underline">
            Clear filters
          </Link>
        )}
      </form>

      {!result.ok ? (
        <ErrorPanel error={result.error} title="Could not load stacks" />
      ) : result.data.data.length === 0 ? (
        <div className="rounded-lg border border-dashed border-slate-300 bg-white p-6 text-sm text-slate-700">
          {filtered || query.cursor ? (
            <>
              No stacks match.{" "}
              <Link href="/stacks" className="text-indigo-700 hover:underline">
                Show all stacks
              </Link>
              .
            </>
          ) : (
            <>
              No stacks yet.{" "}
              <Link href="/catalog" className="text-indigo-700 hover:underline">
                Request a module from the catalog
              </Link>
              .
            </>
          )}
        </div>
      ) : (
        <>
          <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white">
            <table className="min-w-full text-left text-sm">
              <caption className="sr-only">Stacks, newest first</caption>
              <thead className="bg-slate-50 text-xs uppercase tracking-wide text-slate-600">
                <tr>
                  {["Stack", "Status", "Team", "Environment", "Latest request", "Created"].map((h) => (
                    <th key={h} scope="col" className="px-3 py-2">
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {result.data.data.map((s) => (
                  <tr key={s.id}>
                    <th scope="row" className="px-3 py-2 font-medium">
                      <Link href={`/stacks/${encodeURIComponent(s.id)}`} className="text-indigo-700 hover:underline">
                        {s.name}
                      </Link>
                      {s.is_ephemeral && <span className="ml-2 text-xs font-normal text-slate-500">ephemeral</span>}
                    </th>
                    <td className="px-3 py-2">
                      <Badge tone={stackHealth(s.status).tone}>{stackStatusLabel(s.status)}</Badge>
                    </td>
                    <td className="px-3 py-2">{s.team}</td>
                    <td className="px-3 py-2">{s.environment}</td>
                    <td className="px-3 py-2">
                      {s.latest_request ? (
                        <span className="inline-flex flex-wrap items-center gap-2">
                          <Link
                            href={`/requests/${encodeURIComponent(s.latest_request.id)}`}
                            className="font-mono text-xs text-indigo-700 hover:underline"
                          >
                            {s.latest_request.short_id}
                          </Link>
                          <span className="text-xs text-slate-600">{s.latest_request.kind}</span>
                          <StatusBadge status={s.latest_request.status} label={requestStatusLabel(s.latest_request.status)} />
                        </span>
                      ) : (
                        <span className="text-slate-500">none</span>
                      )}
                    </td>
                    <td className="whitespace-nowrap px-3 py-2 text-slate-700">{formatDateTime(s.created_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <nav aria-label="Stack pages" className="flex flex-wrap gap-3 text-sm">
            {query.cursor && (
              <Link
                href={stacksHref({ ...query, cursor: undefined })}
                className="rounded-md border border-slate-300 bg-white px-3 py-1.5 hover:bg-slate-100"
              >
                Back to newest
              </Link>
            )}
            {result.data.next_cursor && (
              <Link
                href={stacksHref({ ...query, cursor: result.data.next_cursor })}
                className="rounded-md border border-slate-300 bg-white px-3 py-1.5 hover:bg-slate-100"
              >
                Older stacks
              </Link>
            )}
          </nav>
        </>
      )}
    </section>
  );
}
