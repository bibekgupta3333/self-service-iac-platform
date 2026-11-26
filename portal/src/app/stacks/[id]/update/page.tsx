import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";

import { ErrorPanel } from "@/components/ErrorPanel";
import { RequestForm, type UpdateContext } from "@/components/request/RequestForm";
import { serverApi, serverAuth } from "@/lib/api/server";
import { attempt, isNotFound } from "@/lib/api/result";
import type { ResourceInput } from "@/lib/api/types";
import { currentSpecRequest } from "@/lib/request/lifecycle";
import { SchemaAdapter } from "@/lib/schema/adapter";
import { stackActions } from "@/lib/stacks";

export const dynamic = "force-dynamic";

type Props = {
  params: Promise<{ id: string }>;
  searchParams: Promise<{ resource?: string; module?: string }>;
};

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  return { title: `Update stack ${(await params).id.slice(0, 8)}` };
}

export default async function UpdateStackPage({ params, searchParams }: Props) {
  const { id } = await params;
  const sp = await searchParams;
  const api = serverApi();

  const stackResult = await attempt(() => api.getStack(id));
  if (!stackResult.ok && isNotFound(stackResult.error)) notFound();
  if (!stackResult.ok) return <ErrorPanel error={stackResult.error} title="Could not load the stack" />;
  const stack = stackResult.data;

  const back = (
    <p className="text-sm">
      <Link href="/stacks" className="text-indigo-700 hover:underline">
        Stacks
      </Link>{" "}
      <span aria-hidden="true">/</span>{" "}
      <Link href={`/stacks/${encodeURIComponent(stack.id)}`} className="font-mono text-indigo-700 hover:underline">
        {stack.name}
      </Link>{" "}
      <span aria-hidden="true">/</span> update
    </p>
  );

  const actions = stackActions(stack, await serverAuth().capabilities());
  if (!actions.update.enabled) {
    return (
      <section aria-labelledby="update-title" className="space-y-4">
        {back}
        <h1 id="update-title" className="text-2xl font-semibold tracking-tight">
          Update {stack.name}
        </h1>
        <p role="alert" className="rounded-lg border border-amber-300 bg-amber-50 p-4 text-sm text-amber-900">
          {actions.update.reason}
        </p>
      </section>
    );
  }

  // The stack's current resources come from the spec of the request that carries it.
  let specRequestId = currentSpecRequest(stack, []);
  if (!specRequestId) {
    const recent = await attempt(() => api.listRequests({ team: stack.team, limit: 100 }));
    if (recent.ok) specRequestId = currentSpecRequest(stack, recent.data.data);
  }
  const specResult = specRequestId ? await attempt(() => api.getRequest(specRequestId)) : undefined;
  const current: ResourceInput[] | undefined =
    specResult?.ok && specResult.data.spec?.resources.length ? specResult.data.spec.resources : undefined;

  let resources: ResourceInput[];
  let editedIndex = 0;
  let moduleName: string;
  let versionId: string;
  let hasSpec = false;
  if (current) {
    hasSpec = true;
    resources = current;
    const wanted = current.findIndex((r) => r.name === sp.resource);
    editedIndex = wanted >= 0 ? wanted : 0;
    const edited = current[editedIndex] as ResourceInput;
    moduleName = edited.module;
    versionId = edited.version;
  } else if (sp.module) {
    // The API returned no spec for this stack: start from a module the user picks.
    const mod = await attempt(() => api.getModule(sp.module as string));
    if (!mod.ok && isNotFound(mod.error)) notFound();
    if (!mod.ok) return <ErrorPanel error={mod.error} title="Could not load the module" />;
    if (!mod.data.latest_version) return <ErrorPanel error={new Error(`${sp.module} has no usable version`)} />;
    moduleName = mod.data.name;
    versionId = mod.data.latest_version;
    resources = [];
  } else {
    const modules = await attempt(() => api.listModules({ status: "active" }));
    return (
      <section aria-labelledby="update-title" className="space-y-4">
        {back}
        <h1 id="update-title" className="text-2xl font-semibold tracking-tight">
          Update {stack.name}
        </h1>
        <p role="note" className="rounded-lg border border-amber-300 bg-amber-50 p-4 text-sm text-amber-900">
          The platform did not return this stack&apos;s current settings, so the form cannot start from what is deployed. Pick
          the module to edit; the form then starts from its defaults.
        </p>
        {modules.ok ? (
          <ul className="space-y-2">
            {modules.data.map((m) => (
              <li key={m.name}>
                <Link
                  href={`/stacks/${encodeURIComponent(stack.id)}/update?module=${encodeURIComponent(m.name)}`}
                  className="text-indigo-700 hover:underline"
                >
                  {m.display_name || m.name}
                </Link>
              </li>
            ))}
          </ul>
        ) : (
          <ErrorPanel error={modules.error} title="Could not load the catalog" />
        )}
      </section>
    );
  }

  const version = await attempt(() => api.getVersion(moduleName, versionId));
  if (!version.ok && isNotFound(version.error)) {
    return (
      <ErrorPanel
        error={new Error(`${moduleName}@${versionId} is no longer in the catalog, so its inputs cannot be shown.`)}
        title="Could not build the update form"
      />
    );
  }
  if (!version.ok) return <ErrorPanel error={version.error} title="Could not load the module version" />;
  const v = version.data;

  if (!hasSpec) {
    resources = [{ name: new SchemaAdapter(v).defaultResourceName(), module: v.module, version: v.version, inputs: {} }];
  }
  const edited = resources[editedIndex] as ResourceInput;
  const context: UpdateContext = {
    stack: { id: stack.id, name: stack.name, team: stack.team, environment: stack.environment, path: stack.path },
    resources,
    editedIndex,
    initialInputs: hasSpec ? edited.inputs : null,
  };

  return (
    <section aria-labelledby="update-title" className="space-y-6">
      <header className="space-y-2">
        {back}
        <h1 id="update-title" className="text-2xl font-semibold tracking-tight">
          Update {stack.name}
        </h1>
        <p className="text-sm text-slate-600">
          Editing <span className="font-mono">{edited.name}</span> ({v.module} v{v.version}). Submitting opens a pull request;
          nothing changes until it is reviewed and merged.
        </p>
        {resources.length > 1 && (
          <nav aria-label="Resources of this stack">
            <ul className="flex flex-wrap gap-2 text-sm">
              {resources.map((r, i) => (
                <li key={r.name}>
                  <Link
                    href={`/stacks/${encodeURIComponent(stack.id)}/update?resource=${encodeURIComponent(r.name)}`}
                    aria-current={i === editedIndex ? "page" : undefined}
                    className={`rounded-full px-3 py-1 ring-1 ring-inset ${
                      i === editedIndex ? "bg-slate-900 text-white ring-slate-900" : "bg-white ring-slate-300 hover:bg-slate-100"
                    }`}
                  >
                    {r.name}
                  </Link>
                </li>
              ))}
            </ul>
          </nav>
        )}
        {v.status === "deprecated" && (
          <p className="rounded-md bg-amber-50 p-3 text-sm text-amber-900">
            This module version is deprecated. Upgrading it is a separate request.
          </p>
        )}
      </header>
      <RequestForm
        key={`${stack.id}:${edited.name}`}
        version={{ module: v.module, version: v.version, inputs_schema: v.inputs_schema }}
        update={context}
      />
    </section>
  );
}
