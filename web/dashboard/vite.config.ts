import { writeFileSync } from "node:fs";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

// go:embed needs dist/ to hold a file even before the first build, so the
// tracked placeholder is put back after Vite empties the directory.
const keepPlaceholder = (): Plugin => ({
  name: "keep-placeholder",
  closeBundle() {
    writeFileSync(new URL("./dist/.placeholder", import.meta.url), "");
  },
});

// Served at /admin/ui/ under a CSP of script-src 'self': no inline script,
// so the module-preload polyfill (an inline snippet) stays off.
export default defineConfig({
  base: "/admin/ui/",
  plugins: [react(), keepPlaceholder()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    assetsDir: "assets",
    modulePreload: { polyfill: false },
    target: "es2022",
    sourcemap: false,
  },
  server: {
    // `npm run dev` against a local relay's admin listener.
    proxy: { "/admin/api": "http://127.0.0.1:5501" },
  },
});
