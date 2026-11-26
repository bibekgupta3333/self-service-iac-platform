import type { Metadata } from "next";
import Link from "next/link";
import type { ReactNode } from "react";

import { Nav } from "@/components/Nav";

import "./globals.css";

export const metadata: Metadata = {
  title: { default: "IaC Platform", template: "%s · IaC Platform" },
  description: "Request versioned Terraform modules and track their lifecycle.",
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen font-sans">
        <a href="#main" className="skip-link">
          Skip to content
        </a>
        <header className="border-b border-slate-200 bg-white">
          <div className="mx-auto flex max-w-6xl items-center justify-between gap-4 px-4 py-3">
            <div className="flex items-center gap-6">
              <Link href="/" className="text-base font-semibold tracking-tight">
                IaC Platform
              </Link>
              <Nav />
            </div>
          </div>
        </header>
        <main id="main" className="mx-auto max-w-6xl px-4 py-8">
          {children}
        </main>
      </body>
    </html>
  );
}
