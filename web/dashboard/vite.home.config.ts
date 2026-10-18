import { writeFileSync } from "node:fs";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

// The relay's public homepage: served at "/" with its assets under /_home/.
const keepPlaceholder = (): Plugin => ({
  name: "keep-placeholder",
  closeBundle() {
    writeFileSync(new URL("./dist-home/.placeholder", import.meta.url), "");
  },
});

// The homepage is a static read of the relay: Preact's React-compatible API
// renders it at a fifth of React's size. The dashboard keeps React.
export default defineConfig({
  base: "/_home/",
  plugins: [react(), keepPlaceholder()],
  resolve: {
    alias: {
      react: "preact/compat",
      "react-dom/client": "preact/compat/client",
      "react-dom": "preact/compat",
      "react/jsx-runtime": "preact/jsx-runtime",
    },
  },
  build: {
    outDir: "dist-home",
    emptyOutDir: true,
    assetsDir: "assets",
    modulePreload: { polyfill: false },
    target: "es2022",
    rollupOptions: { input: "home.html" },
  },
});
