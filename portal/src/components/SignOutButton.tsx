"use client";

import { useState } from "react";

import { browserApiBaseUrl } from "@/lib/api/config";
import { GithubAuthProvider } from "@/lib/auth/provider";

/** SignOutButton revokes the session through the proxy (with the CSRF token) and reloads. */
export function SignOutButton({ onDone }: { onDone?: () => void }) {
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);

  async function signOut() {
    setBusy(true);
    setFailed(false);
    try {
      const provider = new GithubAuthProvider({
        baseUrl: browserApiBaseUrl({ NEXT_PUBLIC_IACP_API_URL: process.env.NEXT_PUBLIC_IACP_API_URL }),
        fetch: (input, init) => fetch(input, init),
      });
      await provider.logout();
      (onDone ?? (() => window.location.reload()))();
    } catch {
      setFailed(true);
      setBusy(false);
    }
  }

  return (
    <button
      type="button"
      onClick={signOut}
      disabled={busy}
      className="rounded-md px-2 py-1 text-xs font-medium text-slate-700 underline hover:bg-slate-200 disabled:opacity-50"
    >
      {failed ? "Sign out failed, retry" : "Sign out"}
    </button>
  );
}
