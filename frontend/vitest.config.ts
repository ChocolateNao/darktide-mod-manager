import react from "@vitejs/plugin-react";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";

// The project root, resolved from the config file, not `__dirname`, which
// the native config loader does not provide.
const root = fileURLToPath(new URL(".", import.meta.url));

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": `${root}src`,
      // Generated bindings sit outside node_modules and are refreshed by
      // `wails generate module`; Vite does not read tsconfig paths on its own,
      // so the same alias is repeated here for the test runner.
      wailsjs: `${root}wailsjs`,
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
    coverage: {
      provider: "v8",
      reporter: ["text", "html"],
      include: [
        "src/bridge/**",
        "src/stores/**",
        "src/lib/schemas/**",
        "src/lib/mappers/**",
      ],
      thresholds: {
        lines: 60,
        functions: 60,
        branches: 50,
        statements: 60,
      },
    },
  },
});
