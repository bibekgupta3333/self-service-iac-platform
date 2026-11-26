import { isApiError } from "@/lib/api/errors";
import { loginUrl } from "@/lib/auth/provider";

/** ErrorPanel shows a failed API call with the backend's request id for support. */
export function ErrorPanel({ error, title = "Something went wrong" }: { error: unknown; title?: string }) {
  const api = isApiError(error) ? error : undefined;
  return (
    <div role="alert" className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-900">
      <p className="font-medium">{api ? api.problem.title : title}</p>
      <p className="mt-1">{api ? (api.problem.detail ?? api.code) : error instanceof Error ? error.message : String(error)}</p>
      {api?.code === "network_error" && (
        <p className="mt-2">
          Is the API running? Start it with <code className="font-mono">make up &amp;&amp; make api</code>, or set{" "}
          <code className="font-mono">IACP_API_URL</code>.
        </p>
      )}
      {api?.problem.status === 401 && (
        <p className="mt-2">
          Your session is missing or has expired.{" "}
          <a href={loginUrl("/")} className="underline">
            Sign in with GitHub
          </a>
          .
        </p>
      )}
      {api?.requestId && (
        <p className="mt-2 text-xs text-red-800">
          Request ID: <code className="font-mono">{api.requestId}</code>
        </p>
      )}
    </div>
  );
}
