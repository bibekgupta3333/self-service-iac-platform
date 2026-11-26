import Link from "next/link";

import type { ActionState } from "@/lib/stacks";

const BASE = "inline-flex items-center rounded-md px-4 py-2 text-sm font-medium";

/**
 * StackActionLink is a stack action that navigates. When it is not available it renders as a
 * disabled button that stays in the tab order (aria-disabled, so a screen reader reads it and
 * its reason), instead of vanishing.
 */
export function StackActionLink({
  href,
  label,
  state,
  describedBy,
}: {
  href: string;
  label: string;
  state: ActionState;
  describedBy?: string;
}) {
  if (state.enabled) {
    return (
      <Link href={href} className={`${BASE} bg-indigo-600 text-white hover:bg-indigo-700`}>
        {label}
      </Link>
    );
  }
  return (
    <button
      type="button"
      aria-disabled="true"
      aria-describedby={describedBy}
      onClick={(e) => e.preventDefault()}
      className={`${BASE} cursor-not-allowed bg-slate-200 text-slate-600`}
    >
      {label}
    </button>
  );
}
