import path from "node:path";

const DEV_PORT = 5173;
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// https://vite.dev/config/
export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  plugins: [react(), tailwindcss()],
  server: {
    port: DEV_PORT,
    proxy: {
      "/supervisor.v1.": {
        // Debug facility (make debug-stack): point the dev frontend at a
        // seeded local backend instead of a real deployment on 8090.
        target: process.env.SUPERVISOR_PROXY_URL ?? "http://localhost:8090",
        changeOrigin: true,
        // The supervisor enforces strict same-origin on browser requests
        // (internal/server/server.go): declare the dev origin via the
        // X-Forwarded-Host header it trusts from reverse-proxy setups.
        headers: { "X-Forwarded-Host": `localhost:${DEV_PORT}` },
      },
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: "./src/test/setup.ts",
  },
});
