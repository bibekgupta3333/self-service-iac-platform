import { SignOutButton } from "@/components/SignOutButton";
import { loginUrl, type Viewer } from "@/lib/auth/provider";

export function UserBadge({ viewer }: { viewer: Viewer }) {
  if (viewer.mode === "github" && viewer.anonymous) {
    return (
      <p className="text-sm">
        {/* A plain anchor: the login route is a redirect, not a page. */}
        <a href={loginUrl("/")} className="rounded-md bg-slate-900 px-3 py-2 font-medium text-white">
          Sign in with GitHub
        </a>
      </p>
    );
  }
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
      {viewer.mode === "github" && <SignOutButton />}
    </p>
  );
}
