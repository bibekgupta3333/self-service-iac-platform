import type { ReactNode } from "react";

import type { Tone } from "@/lib/format";

const TONE_CLASSES: Record<Tone, string> = {
  neutral: "bg-slate-100 text-slate-800 ring-slate-300",
  info: "bg-sky-50 text-sky-900 ring-sky-300",
  progress: "bg-indigo-50 text-indigo-900 ring-indigo-300",
  success: "bg-emerald-50 text-emerald-900 ring-emerald-300",
  warning: "bg-amber-50 text-amber-900 ring-amber-300",
  danger: "bg-red-50 text-red-900 ring-red-300",
};

export function Badge({ tone, children }: { tone: Tone; children: ReactNode }) {
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ring-1 ring-inset ${TONE_CLASSES[tone]}`}
    >
      {children}
    </span>
  );
}
