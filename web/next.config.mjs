import createNextIntlPlugin from "next-intl/plugin";

import { fileURLToPath } from "node:url";

// Static export: `NEXT_EXPORT=1 next build` emits a pure static directory into web/out that can be dropped straight into
// an nginx web root and served. Development (next dev) does not set the variable, keeping the /api rewrite and hot reload.
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel demo: the whole site runs on mocks, no backend, no /api rewrite needed.
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

/** @type {import('next').NextConfig} */
const nextConfig = {
  // Keep a parent-directory lockfile from affecting root inference and asset path generation.
  turbopack: { root: fileURLToPath(new URL(".", import.meta.url)) },
  reactCompiler: true,
  // Development-only cross-origin allowance for `next dev` (HMR + /_next/*). Next refuses a bare "*",
  // so the previous value was the IPv4 wildcard "*.*.*.*": every IPv4 origin could drive dev/HMR
  // against a laptop exposed on a LAN. Loopback stays the default (Next allows it anyway); LAN work
  // is an explicit opt-in with the hosts you actually use, e.g.
  //
  //   ARTEX_DEV_ORIGINS="192.168.1.20,*.mylan.lan" pnpm dev
  //
  // Comma-separated, same syntax Next accepts for allowedDevOrigins. Ignored by the static export
  // (NEXT_EXPORT=1) and by mock builds, which never run the dev server.
  allowedDevOrigins: (process.env.ARTEX_DEV_ORIGINS ?? "localhost,127.0.0.1")
    .split(",")
    .map((origin) => origin.trim())
    .filter(Boolean),
  compiler: {
    removeConsole: process.env.NODE_ENV === "production",
  },
  ...(isExport
    ? {
        // Pure static export: no Node runtime; images are not optimized; each route emits <route>/index.html.
        output: "export",
        images: { unoptimized: true },
        trailingSlash: true,
      }
    : isMock
      ? {
          // Vercel mock demo: no backend, no /api rewrite needed.
          images: { unoptimized: true },
        }
      : {
          // Development: rewrite /api/* to the Go backend (default :8787, override with AUTOPENTEST_API).
          async rewrites() {
            const backend = process.env.AUTOPENTEST_API ?? "http://localhost:8787";
            return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
          },
        }),
};

// next-intl plugin. Request config lives in src/i18n/request.ts. Since i18n path routing is
// not used, no middleware is added (compatible with static export output: "export").
const withNextIntl = createNextIntlPlugin("./src/i18n/request.ts");

export default withNextIntl(nextConfig);
