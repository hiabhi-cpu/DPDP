// Config for the live-stack suite in `live/`. Kept separate from vite.config.ts
// so `npm test` never picks these up: they need the docker stack running and
// they stop/start real containers.
//
// Run them via `npm run test:live` (which stages the patients and codes they
// need) — not directly, or they will fail on missing CODE_* env vars.
import { defineConfig, mergeConfig } from "vitest/config";
import base from "./vite.config";

export default mergeConfig(
  base,
  defineConfig({
    test: {
      include: ["live/**/*.livetest.tsx"],
      // Each test drives a real outage: container stop/start plus a retry
      // budget that can burn ~17s on a hung connection.
      testTimeout: 90_000,
      hookTimeout: 90_000,
      // Containers are shared mutable state — one file at a time.
      fileParallelism: false,
    },
  }),
);
