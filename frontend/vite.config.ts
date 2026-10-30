import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import path from "path";
import { defineConfig } from "vite";

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(import.meta.dirname, "./src"),
      // `wailsjs/` is generated into the project and Vite
      // does not read tsconfig's paths, so the same alias the typechecker needs
      // has to be repeated here or the dev server cannot resolve a single
      // bridge import.
      wailsjs: path.resolve(import.meta.dirname, "./wailsjs"),
    },
  },
});
