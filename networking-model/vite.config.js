import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

export default defineConfig({
  plugins: [svelte()],
  base: "./",
  build: {
    cssCodeSplit: false,
    modulePreload: false,
    assetsInlineLimit: 1000000,
  },
});
