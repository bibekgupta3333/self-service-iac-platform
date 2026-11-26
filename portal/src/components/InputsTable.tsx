import { constraintsOf, type FieldGroup, formatDefault } from "@/lib/schema/fields";

/** InputsTable documents a module version's inputs, one table section per group. */
export function InputsTable({ groups }: { groups: readonly FieldGroup[] }) {
  if (groups.length === 0) {
    return <p className="text-sm text-slate-600">This version declares no inputs.</p>;
  }
  return (
    <div className="overflow-x-auto rounded-lg border border-slate-200 bg-white">
      <table className="min-w-full text-left text-sm">
        <caption className="sr-only">Module inputs</caption>
        <thead className="bg-slate-50 text-xs uppercase tracking-wide text-slate-600">
          <tr>
            <th scope="col" className="px-3 py-2">
              Name
            </th>
            <th scope="col" className="px-3 py-2">
              Type
            </th>
            <th scope="col" className="px-3 py-2">
              Default
            </th>
            <th scope="col" className="px-3 py-2">
              Description
            </th>
          </tr>
        </thead>
        {groups.map((group) => (
          <tbody key={group.name} className="divide-y divide-slate-100">
            <tr className="bg-slate-50/60">
              <th scope="colgroup" colSpan={4} className="px-3 py-1.5 text-xs font-semibold text-slate-700">
                {group.name}
              </th>
            </tr>
            {group.fields.map((f) => {
              const constraints = constraintsOf(f);
              return (
                <tr key={f.name} className="align-top">
                  <th scope="row" className="whitespace-nowrap px-3 py-2 font-mono text-xs font-medium">
                    {f.name}
                    {f.required && (
                      <span className="ml-1 text-red-700" title="required">
                        *<span className="sr-only"> (required)</span>
                      </span>
                    )}
                  </th>
                  <td className="whitespace-nowrap px-3 py-2 font-mono text-xs">{f.typeLabel}</td>
                  <td className="px-3 py-2 font-mono text-xs break-all">{formatDefault(f)}</td>
                  <td className="px-3 py-2 text-slate-700">
                    {f.description ?? <span className="text-slate-400">—</span>}
                    {constraints.length > 0 && (
                      <ul className="mt-1 flex flex-wrap gap-1">
                        {constraints.map((c) => (
                          <li key={c} className="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[11px] text-slate-700 break-all">
                            {c}
                          </li>
                        ))}
                      </ul>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        ))}
      </table>
    </div>
  );
}
