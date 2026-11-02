import { readFile, writeFile } from "node:fs/promises";
import { resolve } from "node:path";

// Inline Vite's compiled output so the artifact works offline over file://.
let html = await readFile("dist/index.html", "utf8");
for (const match of [
  ...html.matchAll(/<script\b[^>]*src="([^"]+)"[^>]*><\/script>/g),
]) {
  const source = await readFile(resolve("dist", match[1]), "utf8");
  html = html.replace(
    match[0],
    () =>
      `<script type="module">${source.replace(/<\/script/gi, "<\\/script")}</script>`,
  );
}
for (const match of [
  ...html.matchAll(/<link\b[^>]*href="([^"]+\.css)"[^>]*>/g),
]) {
  const source = await readFile(resolve("dist", match[1]), "utf8");
  html = html.replace(match[0], () => `<style>${source}</style>`);
}
await writeFile("dist/networking-model.html", html);
console.log("Standalone artifact: dist/networking-model.html");
