import { requestStatusTone, type StepState, timeline } from "@/lib/format";

import { Badge } from "./Badge";

const DOT: Record<StepState, string> = {
  done: "bg-emerald-600 border-emerald-600",
  current: "bg-white border-indigo-600 ring-4 ring-indigo-100",
  upcoming: "bg-white border-slate-300",
  unknown: "bg-slate-200 border-slate-300",
  stopped: "bg-red-600 border-red-600",
};

const STATE_TEXT: Record<StepState, string> = {
  done: "completed",
  current: "current step",
  upcoming: "not started",
  unknown: "not recorded",
  stopped: "outcome",
};

export function StatusBadge({ status, label }: { status: string; label: string }) {
  return <Badge tone={requestStatusTone(status)}>{label}</Badge>;
}

/** StatusTimeline places the request's current status on the request state machine. */
export function StatusTimeline({ status }: { status: string }) {
  const steps = timeline(status);
  return (
    <ol className="flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:gap-0" aria-label="Request progress">
      {steps.map((step, i) => (
        <li
          key={step.status}
          aria-current={step.state === "current" ? "step" : undefined}
          className="flex items-center gap-2 sm:flex-1"
        >
          <span aria-hidden="true" className={`h-3 w-3 shrink-0 rounded-full border-2 ${DOT[step.state]}`} />
          <span
            className={`text-sm ${
              step.state === "current" || step.state === "stopped"
                ? "font-semibold text-slate-900"
                : step.state === "done"
                  ? "text-slate-800"
                  : "text-slate-500"
            }`}
          >
            {step.label}
            <span className="sr-only"> ({STATE_TEXT[step.state]})</span>
          </span>
          {i < steps.length - 1 && <span aria-hidden="true" className="hidden h-px flex-1 bg-slate-300 sm:mx-2 sm:block" />}
        </li>
      ))}
    </ol>
  );
}
