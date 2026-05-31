import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Build output goes to dist/, which is committed and embedded into the grove
// binary via //go:embed. In dev, `npm run dev` proxies /api to a running
// `grove serve --web` so the SPA talks to a real forest.
export default defineConfig({
  plugins: [react()],
  base: "/",
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:8799",
    },
  },
});
