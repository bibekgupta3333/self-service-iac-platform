import type { NextConfig } from "next";

import { contentSecurityPolicy } from "./src/lib/security/csp";

const securityHeaders = [
  {
    key: "Content-Security-Policy",
    value: contentSecurityPolicy({
      dev: process.env.NODE_ENV === "development",
      publicApiUrl: process.env.NEXT_PUBLIC_IACP_API_URL,
    }),
  },
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "X-Frame-Options", value: "DENY" },
  { key: "Referrer-Policy", value: "same-origin" },
];

const nextConfig: NextConfig = {
  reactStrictMode: true,
  poweredByHeader: false,
  async headers() {
    return [{ source: "/:path*", headers: securityHeaders }];
  },
};

export default nextConfig;
