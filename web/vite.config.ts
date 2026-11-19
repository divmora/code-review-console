import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The build is embedded in the Go binary (internal/admin/ui) and served
// under /admin; assets live at /admin/assets. Dev: `npm run dev` proxies the
// API to a console running on :8080.
export default defineConfig({
  base: "/admin/",
  plugins: [react()],
  build: {
    outDir: "../internal/admin/ui",
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
  },
  server: {
    proxy: { "/admin/api": "http://localhost:8080", "/admin/login": "http://localhost:8080", "/admin/logout": "http://localhost:8080" },
  },
});
