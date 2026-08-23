/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  // The dashboard is a pure client of services/observability (obsplane); it owns
  // no server-side secrets, so nothing here proxies or rewrites to the API.
  // The browser talks to NEXT_PUBLIC_OBS_API_BASE directly (CORS is obsplane's call).
};
export default nextConfig;
