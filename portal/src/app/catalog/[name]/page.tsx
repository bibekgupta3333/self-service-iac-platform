import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";

import { Badge } from "@/components/Badge";
import { ErrorPanel } from "@/components/ErrorPanel";
import { InputsTable } from "@/components/InputsTable";
import { serverApi } from "@/lib/api/factory";
import { attempt, isNotFound } from "@/lib/api/result";
import type { ModuleVersion } from "@/lib/api/types";
import { catalogStatusTone, formatDateTime, shortSha } from "@/lib/format";
import { SchemaAdapter } from "@/lib/schema/adapter";

export const dynamic = "force-dynamic";

type Props = {
  params: Promise<{ name: string }>;
  searchParams: Promise<{ version?: string }>;
};

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  return { title: (await params).name };
}

export default async function ModulePage({ params, searchParams }: Props) {
  const name = (await params).name;
  const { version: requested } = await searchParams;
  const api = serverApi();

  const [mod, versions] = await Promise.all([attempt(() => api.getModule(name)), attempt(() => api.listVersions(name))]);
  if (!mod.ok && isNotFound(mod.error)) notFound();
  if (!mod.ok) return <ErrorPanel error={mod.error} title="Could not load the module" />;

  const versionList = versions.ok ? versions.data : [];
  const selected = requested ?? mod.data.latest_version ?? versionList[0]?.version;
  let detail: { ok: true; data: ModuleVersion } | { ok: false; error: unknown } | undefined;
  if (selected) {
    detail =
      mod.data.latest && mod.data.latest.version === selected
        ? { ok: true, data: mod.data.latest }
        : await attempt(() => api.getVersion(name, selected));
  }

  const m = mod.data;
  return (
    <article className="space-y-8" aria-labelledby="module-title">
      <header className="space-y-2">
        <p className="text-sm">
          <Link href="/catalog" className="text-indigo-700 hover:underline">
            Catalog
          </Link>{" "}
          <span aria-hidden="true">/</span> <span className="font-mono">{m.name}</span>
        </p>
        <div className="flex flex-wrap items-center gap-3">
          <h1 id="module-title" className="text-2xl font-semibold tracking-tight">
            {m.display_name || m.name}
          </h1>
          <Badge tone={catalogStatusTone(m.status)}>{m.status}</Badge>
        </div>
        <p className="max-w-3xl text-slate-700">{m.description}</p>
        <dl className="flex flex-wrap gap-x-6 gap-y-1 text-sm text-slate-600">
          <div>
            <dt className="inline font-medium">Category: </dt>
            <dd className="inline">{m.category}</dd>
          </div>
          <div>
            <dt className="inline font-medium">Owners: </dt>
            <dd className="inline">{m.owners.join(", ") || "—"}</dd>
          </div>
          <div>
            <dt className="inline font-medium">Path: </dt>
            <dd className="inline font-mono">{m.repo_path}</dd>
          </div>
        </dl>
      </header>

      <section aria-labelledby="versions-title" className="space-y-3">
        <h2 id="versions-title" className="text-lg font-semibold">
          Versions
        </h2>
        {!versions.ok ? (
          <ErrorPanel error={versions.error} title="Could not load versions" />
        ) : versionList.length === 0 ? (
          <p className="text-sm text-slate-600">No versions have been released.</p>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white">
            <table className="min-w-full text-left text-sm">
              <caption className="sr-only">Released versions, newest first</caption>
              <thead className="bg-slate-50 text-xs uppercase tracking-wide text-slate-600">
                <tr>
                  <th scope="col" className="px-3 py-2">
                    Version
                  </th>
                  <th scope="col" className="px-3 py-2">
                    Status
                  </th>
                  <th scope="col" className="px-3 py-2">
                    Released
                  </th>
                  <th scope="col" className="px-3 py-2">
                    Notes
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {versionList.map((v) => (
                  <tr key={v.version} className={v.version === selected ? "bg-indigo-50/50" : undefined}>
                    <th scope="row" className="px-3 py-2 font-mono text-xs font-medium">
                      <Link
                        href={`/catalog/${encodeURIComponent(m.name)}?version=${encodeURIComponent(v.version)}`}
                        aria-current={v.version === selected ? "true" : undefined}
                        className="text-indigo-700 hover:underline"
                      >
                        v{v.version}
                      </Link>
                    </th>
                    <td className="px-3 py-2">
                      <Badge tone={catalogStatusTone(v.status)}>{v.status}</Badge>
                    </td>
                    <td className="px-3 py-2 text-slate-700">{formatDateTime(v.released_at)}</td>
                    <td className="px-3 py-2 text-slate-700">{v.deprecation_note ?? ""}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {detail && !detail.ok && <ErrorPanel error={detail.error} title={`Could not load version ${selected}`} />}
      {detail?.ok && <VersionDetail module={m.name} version={detail.data} />}
    </article>
  );
}

function VersionDetail({ module, version: v }: { module: string; version: ModuleVersion }) {
  const adapter = new SchemaAdapter(v);
  const requestable = v.status !== "yanked";
  return (
    <section aria-labelledby="version-title" className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 id="version-title" className="text-lg font-semibold">
          v{v.version}
        </h2>
        {requestable ? (
          <Link
            href={`/catalog/${encodeURIComponent(module)}/request?version=${encodeURIComponent(v.version)}`}
            className="rounded-md bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700"
          >
            Request v{v.version}
          </Link>
        ) : (
          <p className="text-sm text-red-800">This version is yanked and cannot be requested.</p>
        )}
      </div>

      <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
        <div>
          <dt className="font-medium text-slate-600">Source</dt>
          <dd className="font-mono text-xs break-all">{v.source}</dd>
        </div>
        <div>
          <dt className="font-medium text-slate-600">Commit</dt>
          <dd className="font-mono text-xs" title={v.git_sha}>
            {shortSha(v.git_sha)} ({v.git_tag})
          </dd>
        </div>
        <div>
          <dt className="font-medium text-slate-600">Terraform</dt>
          <dd className="font-mono text-xs">{v.tf_version_range}</dd>
        </div>
        <div>
          <dt className="font-medium text-slate-600">AWS provider</dt>
          <dd className="font-mono text-xs">{v.aws_provider_range}</dd>
        </div>
      </dl>

      <section aria-labelledby="inputs-title" className="space-y-3">
        <h3 id="inputs-title" className="text-base font-semibold">
          Inputs
        </h3>
        <InputsTable groups={adapter.groups()} />
      </section>

      <section aria-labelledby="outputs-title" className="space-y-3">
        <h3 id="outputs-title" className="text-base font-semibold">
          Outputs
        </h3>
        {v.outputs.length === 0 ? (
          <p className="text-sm text-slate-600">No outputs.</p>
        ) : (
          <ul className="divide-y divide-slate-100 rounded-lg border border-slate-200 bg-white text-sm">
            {v.outputs.map((o) => (
              <li key={o.name} className="flex flex-wrap items-baseline gap-x-3 px-3 py-2">
                <span className="font-mono text-xs font-medium">{o.name}</span>
                {o.sensitive && <Badge tone="warning">sensitive</Badge>}
                <span className="text-slate-700">{o.description}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </section>
  );
}
