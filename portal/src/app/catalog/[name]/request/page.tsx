import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";

import { ErrorPanel } from "@/components/ErrorPanel";
import { RequestForm } from "@/components/request/RequestForm";
import { serverApi } from "@/lib/api/factory";
import { attempt, isNotFound } from "@/lib/api/result";

export const dynamic = "force-dynamic";

type Props = {
  params: Promise<{ name: string }>;
  searchParams: Promise<{ version?: string }>;
};

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  return { title: `Request ${decodeURIComponent((await params).name)}` };
}

export default async function RequestModulePage({ params, searchParams }: Props) {
  const name = decodeURIComponent((await params).name);
  const { version: requested } = await searchParams;
  const api = serverApi();

  let versionId = requested;
  if (!versionId) {
    const mod = await attempt(() => api.getModule(name));
    if (!mod.ok && isNotFound(mod.error)) notFound();
    if (!mod.ok) return <ErrorPanel error={mod.error} title="Could not load the module" />;
    versionId = mod.data.latest_version ?? undefined;
    if (!versionId) return <ErrorPanel error={new Error(`${name} has no usable version`)} />;
  }

  const version = await attempt(() => api.getVersion(name, versionId));
  if (!version.ok && isNotFound(version.error)) notFound();
  if (!version.ok) return <ErrorPanel error={version.error} title="Could not load the module version" />;
  const v = version.data;

  return (
    <section aria-labelledby="request-title" className="space-y-6">
      <header className="space-y-2">
        <p className="text-sm">
          <Link href="/catalog" className="text-indigo-700 hover:underline">
            Catalog
          </Link>{" "}
          <span aria-hidden="true">/</span>{" "}
          <Link
            href={`/catalog/${encodeURIComponent(v.module)}?version=${encodeURIComponent(v.version)}`}
            className="font-mono text-indigo-700 hover:underline"
          >
            {v.module}
          </Link>{" "}
          <span aria-hidden="true">/</span> request
        </p>
        <h1 id="request-title" className="text-2xl font-semibold tracking-tight">
          Request {v.module} v{v.version}
        </h1>
        {v.status === "deprecated" && (
          <p className="rounded-md bg-amber-50 p-3 text-sm text-amber-900">
            This version is deprecated. Consider a newer version from the module page.
          </p>
        )}
      </header>
      {v.status === "yanked" ? (
        <ErrorPanel error={new Error(`${v.module}@${v.version} is yanked and cannot be requested.`)} />
      ) : (
        <RequestForm version={{ module: v.module, version: v.version, inputs_schema: v.inputs_schema }} />
      )}
    </section>
  );
}
