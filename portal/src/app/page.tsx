import Link from "next/link";

export default function HomePage() {
  return (
    <section aria-labelledby="home-title" className="space-y-6">
      <h1 id="home-title" className="text-2xl font-semibold tracking-tight">
        Self-service infrastructure
      </h1>
      <p className="max-w-2xl text-slate-700">
        Pick a versioned Terraform module from the catalog, fill in its inputs and submit a request. The
        platform opens a pull request, GitHub Actions validates, scans and plans it, and the change is
        applied after review.
      </p>
      <ul className="grid gap-4 sm:grid-cols-2">
        <li>
          <Link
            href="/catalog"
            className="block rounded-lg border border-slate-200 bg-white p-5 hover:border-slate-400"
          >
            <span className="block font-medium">Browse the catalog</span>
            <span className="mt-1 block text-sm text-slate-600">Modules, versions and their inputs.</span>
          </Link>
        </li>
        <li>
          <Link
            href="/requests"
            className="block rounded-lg border border-slate-200 bg-white p-5 hover:border-slate-400"
          >
            <span className="block font-medium">Track requests</span>
            <span className="mt-1 block text-sm text-slate-600">Status of the changes you asked for.</span>
          </Link>
        </li>
      </ul>
    </section>
  );
}
