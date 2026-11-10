/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  async rewrites() {
    return [
      {
        source: '/api/relay/v1/:path*',
        // Override with RELAY_GATEWAY_URL when the gateway is not on :8080
        destination: `${process.env.RELAY_GATEWAY_URL || 'http://localhost:8080'}/v1/:path*`,
      },
    ];
  },
};

export default nextConfig;
