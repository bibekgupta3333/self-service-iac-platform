import type { Viewer } from "@/lib/auth/provider";

export function UserBadge({ viewer }: { viewer: Viewer }) {
  return (
    <p className="flex items-center gap-2 text-sm text-slate-700">
      <span className="sr-only">Signed in as</span>
      <span className="font-medium">{viewer.displayName}</span>
      {viewer.mode === "dev" && (
        <span
          className="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-medium text-amber-900"
          title="The API runs with IACP_AUTH_MODE=dev: every call acts as this local user."
        >
          dev auth
        </span>
      )}
    </p>
  );
}
