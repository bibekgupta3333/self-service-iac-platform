import Link from "next/link";

export default function NotFound() {
  return (
    <section aria-labelledby="nf-title" className="space-y-3">
      <h1 id="nf-title" className="text-2xl font-semibold tracking-tight">
        Not found
      </h1>
      <p className="text-slate-700">The page or resource you asked for does not exist, or you cannot see it.</p>
      <Link href="/catalog" className="text-indigo-700 hover:underline">
        Back to the catalog
      </Link>
    </section>
  );
}
