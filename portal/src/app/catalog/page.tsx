import type { Metadata } from "next";
import Link from "next/link";

import { Badge } from "@/components/Badge";
import { ErrorPanel } from "@/components/ErrorPanel";
import { serverApi } from "@/lib/api/factory";
import { attempt } from "@/lib/api/result";
import { catalogStatusTone, categoriesOf } from "@/lib/format";

export const dynamic = "force-dynamic";
export const metadata: Metadata = { title: "Catalog" };

type Props = { searchParams: Promise<{ category?: string }> };

export default async function CatalogPage({ searchParams }: Props) {
  const { category } = await searchParams;
  // Load every module once: the filter chips come from the data, so a new category shows up
  // without a portal change, and filtering happens here.
  const all = await attempt(() => serverApi().listModules());
  const categories = all.ok ? categoriesOf(all.data) : [];
  const selected = categories.find((c) => c === category);
  const result = all.ok ? { ok: true as const, data: all.data.filter((m) => !selected || m.category === selected) } : all;

  return (
    <section aria-labelledby="catalog-title" className="space-y-6">
      <div>
        <h1 id="catalog-title" className="text-2xl font-semibold tracking-tight">
          Module catalog
        </h1>
        <p className="mt-1 text-slate-600">Released, versioned Terraform modules you can request.</p>
      </div>

      <nav aria-label="Filter by category">
        <ul className="flex flex-wrap gap-2 text-sm">
          {[undefined, ...categories].map((c) => (
            <li key={c ?? "all"}>
              <Link
                href={c ? `/catalog?category=${encodeURIComponent(c)}` : "/catalog"}
                aria-current={c === selected ? "page" : undefined}
                className={`rounded-full px-3 py-1 ring-1 ring-inset ${
                  c === selected ? "bg-slate-900 text-white ring-slate-900" : "bg-white ring-slate-300 hover:bg-slate-100"
                }`}
              >
                {c ?? "All"}
              </Link>
            </li>
          ))}
        </ul>
      </nav>

      {!result.ok ? (
        <ErrorPanel error={result.error} title="Could not load the catalog" />
      ) : result.data.length === 0 ? (
        <div className="rounded-lg border border-dashed border-slate-300 bg-white p-6 text-sm text-slate-700">
          <p className="font-medium">No modules {selected ? `in ${selected}` : "indexed yet"}.</p>
          <p className="mt-1">
            Modules appear here after <code className="font-mono">module-release.yml</code> indexes a tagged release
            (locally: <code className="font-mono">python -m tools.module_release index</code>, see{" "}
            <code className="font-mono">tools/README.md</code>).
          </p>
        </div>
      ) : (
        <ul className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {result.data.map((m) => (
            <li key={m.name}>
              <Link
                href={`/catalog/${encodeURIComponent(m.name)}`}
                className="flex h-full flex-col rounded-lg border border-slate-200 bg-white p-5 hover:border-slate-400"
              >
                <span className="flex items-start justify-between gap-2">
                  <span className="font-medium">{m.display_name || m.name}</span>
                  <Badge tone={catalogStatusTone(m.status)}>{m.status}</Badge>
                </span>
                <span className="mt-1 font-mono text-xs text-slate-500">
                  {m.name}
                  {m.latest_version ? ` · v${m.latest_version}` : " · no usable version"}
                </span>
                <span className="mt-3 line-clamp-3 text-sm text-slate-700">{m.description}</span>
                <span className="mt-auto pt-3 text-xs uppercase tracking-wide text-slate-500">{m.category}</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
