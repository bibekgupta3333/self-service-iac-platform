"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";

/**
 * AutoRefresh re-renders the current server component on an interval while the tab is
 * visible. It stands in for the SSE stream (GET /v1/requests/{id}/events, WBS 7.4).
 */
export function AutoRefresh({ intervalMs, enabled = true }: { intervalMs: number; enabled?: boolean }) {
  const router = useRouter();
  useEffect(() => {
    if (!enabled) return;
    const id = window.setInterval(() => {
      if (document.visibilityState === "visible") router.refresh();
    }, intervalMs);
    return () => window.clearInterval(id);
  }, [router, intervalMs, enabled]);
  return null;
}
