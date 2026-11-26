"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

export interface NavItem {
  href: string;
  label: string;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { href: "/catalog", label: "Catalog" },
  { href: "/stacks", label: "Stacks" },
  { href: "/requests", label: "Requests" },
];

/** isActive reports whether pathname is inside the section at href. Pure. */
export function isActive(pathname: string, href: string): boolean {
  return pathname === href || pathname.startsWith(`${href}/`);
}

export function Nav({ items = NAV_ITEMS }: { items?: readonly NavItem[] }) {
  const pathname = usePathname() ?? "/";
  return (
    <nav aria-label="Main">
      <ul className="flex gap-1">
        {items.map((item) => {
          const active = isActive(pathname, item.href);
          return (
            <li key={item.href}>
              <Link
                href={item.href}
                aria-current={active ? "page" : undefined}
                className={`rounded-md px-3 py-2 text-sm font-medium ${
                  active ? "bg-slate-900 text-white" : "text-slate-700 hover:bg-slate-200"
                }`}
              >
                {item.label}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
