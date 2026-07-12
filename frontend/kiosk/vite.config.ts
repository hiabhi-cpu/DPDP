import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  base: "/kiosk/",
  plugins: [react()],
  build: {
    // ponytail: browser floor for ~5-year-old devices; drop anything older.
    target: ["es2021", "chrome90", "safari14"],
  },
  server: {
    port: 5174,
    proxy: {
      // Same-origin from the browser; Vite forwards /kiosk/api to the BFF.
      "/kiosk/api": { target: "http://localhost:9008", changeOrigin: true },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./vitest.setup.ts"],
  },
});
