#!/usr/bin/env node
import {spawn} from "node:child_process";
import {mkdir} from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import {fileURLToPath, pathToFileURL} from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const frontend = path.resolve(here, "../..");
const playwrightPath = path.resolve(frontend, "../testing-vms/e2e/node_modules/playwright/index.mjs");
const {chromium} = await import(pathToFileURL(playwrightPath).href);

const THEMES = ["slate", "graphite", "classic"];

const args = (() => {
    const out = {theme: "all", shots: "", out: path.join(os.tmpdir(), "shots"), port: 5199, width: 1440, height: 900, scale: 1};
    const argv = process.argv.slice(2);
    for (let i = 0; i < argv.length; i++) {
        const [flag, inline] = argv[i].split("=");
        const key = flag.replace(/^--/, "");
        if (!(key in out)) throw new Error(`unknown flag ${flag}`);
        const value = inline ?? argv[++i];
        out[key] = typeof out[key] === "number" ? Number(value) : value;
    }
    return out;
})();

const nav = (page, key) => page.getByTestId(`nav-${key}`).click();
const tab = (page, id) => page.getByTestId(id).click();
const noLoading = async (page) => {
    await page.waitForFunction(() => !/Loading…|Loading\.\.\./.test(document.body.innerText), null, {timeout: 8000}).catch(() => {});
};
const rowByText = async (page, text, scope = "table") => {
    const row = page.locator(`${scope} tr`, {hasText: text}).first();
    await row.waitFor({timeout: 5000});
    await row.click();
};
const statusRow = (page, name) => page.getByTestId(`deployment-row-${name}`).first();
const pickLogsDeployment = async (page, name, space) => {
    await page.getByTestId("logs-deployment-select").click();
    await page.getByTestId("logs-deployment-select-menu").getByRole("menuitemradio")
        .filter({has: page.getByText(name, {exact: true})}).filter({hasText: space}).first().click();
};

// Each shot: view (app|login|enrol|landing), steps(page), optional clip.
const SHOTS = {
    "status": {steps: async (p) => { await statusRow(p, "billing-cron").waitFor(); }},
    "status-inspector": {steps: async (p) => {
        await statusRow(p, "api").locator("td").first().click();
        await p.getByTestId("deployment-inspector").waitFor();
    }},
    "status-history": {steps: async (p) => {
        await statusRow(p, "api").locator("td").first().click();
        await p.getByTestId("deployment-inspector").getByRole("button", {name: "History"}).click();
        await p.waitForTimeout(400);
    }},
    "status-editor": {steps: async (p) => {
        await statusRow(p, "web").hover();
        await statusRow(p, "web").getByRole("button", {name: "Update"}).click();
        await p.locator("[data-testid^='deployments-tab-']").nth(1).waitFor();
        await p.waitForTimeout(1200);
    }},
    "status-create": {steps: async (p) => {
        await p.getByTestId("add-deployment-button").click();
        await p.locator("[data-testid^='deployments-tab-']").nth(1).waitFor();
        await p.waitForTimeout(1200);
    }},
    "dialog-recently-deleted": {steps: async (p) => {
        await p.getByTestId("recently-deleted-button").click();
        await p.getByText("legacy-admin").first().waitFor();
    }},
    "dialog-run-report": {steps: async (p) => {
        await p.getByTestId("deployment-restarts-open-billing-cron").click();
        await p.getByText("panic: billing").first().waitFor();
    }},
    "status-crash-reason": {steps: async (p) => {
        await p.getByTestId("deployment-row-api").nth(1).locator("td").first().click();
        await p.locator("[data-testid^='deployment-crash-reason-api-']").waitFor();
    }},
    "dialog-run-report-reason": {steps: async (p) => {
        await p.getByTestId("deployment-row-api").nth(1).locator("td").first().click();
        await p.locator("[data-testid^='deployment-crash-reason-api-']").waitFor();
        await p.locator("[data-testid^='instance-rst-open-api-']").first().click();
        await p.getByTestId("run-report-crash-reason").waitFor();
    }},
    "secrets": {steps: async (p) => { await nav(p, "secrets"); await p.getByText("session_signing_key").first().waitFor(); }},
    "secrets-inspector": {steps: async (p) => {
        await nav(p, "secrets");
        await rowByText(p, "postgres_password");
        await p.waitForTimeout(300);
    }},
    "dialog-secret-value": {steps: async (p) => {
        await nav(p, "secrets");
        await p.getByRole("button", {name: "Edit grafana_admin_password"}).click();
        await p.waitForTimeout(800);
    }},
    "assets": {steps: async (p) => { await nav(p, "assets"); await p.getByText("favicon.ico").first().waitFor(); }},
    "assets-inspector": {steps: async (p) => {
        await nav(p, "assets");
        await rowByText(p, "nginx.conf");
        await p.waitForTimeout(600);
    }},
    "logs": {steps: async (p) => {
        await nav(p, "logs");
        await pickLogsDeployment(p, "api", "prod");
        await p.waitForFunction(() => window.__logsResult?.records?.length > 0, null, {timeout: 8000});
        await p.waitForTimeout(400);
    }},
    "deployment-select": {steps: async (p) => {
        await nav(p, "logs");
        await pickLogsDeployment(p, "api", "prod");
        await p.waitForFunction(() => window.__logsResult?.records?.length > 0, null, {timeout: 8000});
        await p.getByTestId("logs-deployment-select").click();
        await p.getByTestId("logs-deployment-select-menu").waitFor();
        await p.getByTestId("logs-deployment-select-menu").getByRole("menuitemradio").nth(3).hover();
    }},
    "deployment-select-search": {steps: async (p) => {
        await nav(p, "logs");
        await p.getByTestId("logs-deployment-select").click();
        await p.getByTestId("logs-deployment-select-search").fill("web st");
    }},
    "metrics": {steps: async (p) => { await nav(p, "metrics"); await p.getByTestId("metrics-overview-table").waitFor(); }},
    "metrics-deployment": {steps: async (p) => {
        await nav(p, "metrics");
        await p.getByTestId("metrics-overview-table").waitFor();
        await p.getByTestId("metrics-overview-row-api").first().click();
        await p.waitForFunction(() => window.__metricsResult?.runs?.length > 0, null, {timeout: 20000});
        await p.getByTestId("metrics-chart-cpu").waitFor();
        await p.waitForTimeout(400);
    }},
    "metrics-nodes": {steps: async (p) => {
        await nav(p, "metrics");
        await tab(p, "metrics-tab-nodes");
        await p.getByTestId("metrics-nodes-table").waitFor();
    }},
    "metrics-node": {steps: async (p) => {
        await nav(p, "metrics");
        await tab(p, "metrics-tab-nodes");
        await p.getByTestId("metrics-nodes-row-worker-a").click();
        await p.waitForFunction(() => window.__metricsNodeResult?.fields > 0, null, {timeout: 20000});
        await p.getByTestId("metrics-node-chart-cpu").waitFor();
        await p.waitForTimeout(400);
    }},
    "users": {steps: async (p) => { await nav(p, "users"); await p.getByTestId("user-row-1").waitFor(); }},
    "users-rules": {steps: async (p) => { await nav(p, "users"); await tab(p, "users-tab-rules"); await p.getByTestId("template-row-1").waitFor(); }},
    "users-you": {steps: async (p) => { await nav(p, "users"); await tab(p, "users-tab-you"); await p.getByText("YubiKey 5C").first().waitFor(); }},
    "sessions": {steps: async (p) => { await nav(p, "sessions"); await p.getByTestId("agent-session-list").waitFor(); }},
    "sessions-user": {steps: async (p) => {
        await nav(p, "sessions");
        await p.getByTestId("sessions-tab-user").or(p.getByRole("tab", {name: /User sessions/})).first().click();
        await p.getByTestId("user-session-list").waitFor();
    }},
    "spaces": {steps: async (p) => { await nav(p, "spaces"); await p.locator("[data-testid^='space-row-']").first().waitFor(); }},
    "spaces-domain": {steps: async (p) => { await nav(p, "spaces"); await tab(p, "spaces-tab-domain-policies"); await p.getByTestId("domain-policy-row-1").waitFor(); }},
    "dialog-add-space": {steps: async (p) => {
        await nav(p, "spaces");
        await p.getByRole("button", {name: "Add space"}).first().click();
        await p.waitForTimeout(300);
    }},
    "policies": {steps: async (p) => { await nav(p, "policies"); await p.getByTestId("network-policy-row-1").waitFor(); }},
    "cluster": {steps: async (p) => {
        await nav(p, "cluster");
        await p.locator("[data-testid^='machine-row-']").first().waitFor();
        await p.locator("[data-testid='node-disk'][data-state='loaded']").first().waitFor();
    }},
    "cluster-disk-loading": {steps: async (p) => {
        await nav(p, "cluster");
        await p.locator("[data-testid='node-disk'][data-state='loading']").first().waitFor();
    }},
    "cluster-inspector": {steps: async (p) => {
        await nav(p, "cluster");
        await p.locator("[data-testid^='machine-row-']", {hasText: "worker-a"}).first().click();
        await p.getByTestId("node-inspector").waitFor();
        await p.getByTestId("node-inspector-disk-data").waitFor();
    }},
    "cluster-access": {steps: async (p) => { await nav(p, "cluster"); await tab(p, "nodes-tab-access"); await p.getByTestId("node-access-policy-row-1").waitFor(); }},
    "dialog-node-access-policy": {steps: async (p) => {
        await nav(p, "cluster");
        await tab(p, "nodes-tab-access");
        await p.getByTestId("node-access-policy-edit-4").click();
        await p.getByTestId("node-access-policy-editor").waitFor();
    }},
    "cluster-enrollments": {steps: async (p) => { await nav(p, "cluster"); await tab(p, "nodes-tab-enrollments"); await p.locator("[data-testid^='enrollment-request-']").first().waitFor(); await noLoading(p); }},
    "cluster-replication": {steps: async (p) => { await nav(p, "cluster"); await tab(p, "nodes-tab-replication"); await p.getByTestId("backup-replication-status").waitFor(); }},
    "settings": {steps: async (p) => { await nav(p, "settings"); await p.getByTestId("settings-page").waitFor(); await p.locator("[data-testid^='setting-row-']").first().waitFor(); }},
    "shell-stream-card": {clip: (w, h) => ({x: 0, y: h - 300, width: 520, height: 300}), steps: async (p) => {
        await p.getByTestId("stream-status").hover();
        await p.getByTestId("stream-status-card").waitFor();
        await p.waitForTimeout(300);
    }},
    "shell-theme-menu": {steps: async (p) => {
        await p.getByTestId("theme-button").click();
        await p.getByTestId("theme-menu").waitFor();
    }},
    "space-filter": {steps: async (p) => {
        await statusRow(p, "billing-cron").waitFor();
        await p.getByTestId("deployments-space-filter").click();
        await p.getByTestId("deployments-space-filter-menu").waitFor();
        await p.getByTestId("deployments-space-filter-row-2").hover();
    }},
    "space-filter-presets": {steps: async (p) => {
        await statusRow(p, "billing-cron").waitFor();
        const id = "deployments-space-filter";
        const save = async (name) => {
            await p.getByTestId(`${id}-preset-add`).click();
            await p.getByTestId(`${id}-preset-name`).fill(name);
            await p.getByTestId(`${id}-preset-save`).click();
        };
        await p.getByTestId(id).click();
        await p.getByTestId(`${id}-row-2`).hover();
        await p.getByTestId(`${id}-only-2`).click();
        await save("prod only");
        await p.getByTestId(`${id}-row-3`).click();
        await save("prod + staging");
        await p.getByTestId(`${id}-all`).click();
        await save("everything");
        await p.locator(`[data-testid='${id}-preset'][data-preset='prod + staging']`).click();
        await p.getByTestId(`${id}-row-1`).click();
        await p.getByTestId(`${id}-preset-add`).click();
        await p.getByTestId(`${id}-preset-name`).fill("prod + st");
    }},
    "space-filter-search": {steps: async (p) => {
        await nav(p, "secrets");
        await p.getByText("session_signing_key").first().waitFor();
        await p.getByTestId("secrets-space-filter").click();
        await p.getByTestId("secrets-space-filter-search").fill("st");
    }},
    "login": {view: "login", steps: async (p) => { await p.getByRole("button", {name: /sign in/i}).first().waitFor(); await noLoading(p); }},
    "enrol": {view: "enrol", steps: async (p) => { await p.locator("input[type=password]").first().waitFor(); }},
    "landing": {view: "landing", steps: async (p) => { await p.getByTestId("landing-name-input").waitFor(); }},
};

const portOpenOn = (port, host) => new Promise((resolve) => {
    const socket = net.connect({port, host});
    socket.once("connect", () => { socket.destroy(); resolve(true); });
    socket.once("error", () => resolve(false));
});
const portOpen = async (port) => (await portOpenOn(port, "127.0.0.1")) || (await portOpenOn(port, "::1"));

const stopVite = (child) => {
    try { process.kill(-child.pid, "SIGTERM"); } catch { child.kill("SIGTERM"); }
};

const startVite = async (port) => {
    if (await portOpen(port)) return null;
    const child = spawn("pnpm", ["exec", "vite", "--port", String(port), "--strictPort"], {cwd: frontend, stdio: ["ignore", "pipe", "pipe"], detached: true});
    let output = "";
    child.stdout.on("data", (d) => { output += d; });
    child.stderr.on("data", (d) => { output += d; });
    for (let i = 0; i < 120; i++) {
        if (child.exitCode != null) throw new Error(`vite exited early:\n${output}`);
        if (await portOpen(port)) return child;
        await new Promise((r) => setTimeout(r, 250));
    }
    stopVite(child);
    throw new Error(`vite did not start on port ${port}:\n${output}`);
};

const themes = args.theme === "all" ? THEMES : args.theme.split(",");
for (const theme of themes) if (!THEMES.includes(theme)) throw new Error(`unknown theme ${theme}`);
const names = args.shots ? args.shots.split(",").map((s) => s.trim()).filter(Boolean) : Object.keys(SHOTS);
for (const name of names) if (!SHOTS[name]) throw new Error(`unknown shot ${name}; known: ${Object.keys(SHOTS).join(", ")}`);

const vite = await startVite(args.port);
const base = `http://localhost:${args.port}/fixtures/app/index.html`;
const browser = await chromium.launch();
const results = [];

try {
    for (const theme of themes) {
        const dir = path.join(args.out, theme);
        await mkdir(dir, {recursive: true});
        for (const name of names) {
            const shot = SHOTS[name];
            const context = await browser.newContext({viewport: {width: args.width, height: args.height}, deviceScaleFactor: args.scale});
            await context.addInitScript((t) => {
                try { localStorage.setItem("opsagent_theme", t); } catch {}
                if (document.documentElement) document.documentElement.dataset.theme = t;
                else document.addEventListener("readystatechange", () => { document.documentElement.dataset.theme = t; }, {once: true});
            }, theme);
            // Vite reloads the page whenever a source file is saved; a mocked
            // socket keeps a shot from being taken mid-reload.
            await context.routeWebSocket(/.*/, () => {});
            const page = await context.newPage();
            page.setDefaultTimeout(10000);
            const problems = [];
            page.on("console", (msg) => { if (msg.type() === "error") problems.push(msg.text()); });
            page.on("pageerror", (err) => problems.push(`pageerror: ${err.message}`));
            const file = path.join(dir, `${name}.png`);
            try {
                await page.goto(`${base}?view=${shot.view || "app"}&chrome=0`, {waitUntil: "load"});
                await page.evaluate((t) => { document.documentElement.dataset.theme = t; }, theme);
                if ((shot.view || "app") === "app") {
                    await page.getByTestId("nav-status").waitFor({timeout: 15000});
                    await page.waitForFunction(() => /healthy|connected/i.test(document.querySelector("[data-testid='stream-status']")?.innerText || document.body.innerText), null, {timeout: 8000}).catch(() => {});
                }
                await shot.steps(page);
                await noLoading(page);
                await page.mouse.move(args.width - 2, 2).catch(() => {});
                if (name === "shell-stream-card") await page.getByTestId("stream-status").hover();
                await page.waitForTimeout(250);
                await page.screenshot({path: file, ...(shot.clip ? {clip: shot.clip(args.width, args.height)} : {})});
                results.push({theme, name, ok: true, file, problems});
            } catch (e) {
                await page.screenshot({path: file}).catch(() => {});
                results.push({theme, name, ok: false, file, error: e.message.split("\n")[0], problems});
            } finally {
                await context.close();
            }
        }
    }
} finally {
    await browser.close();
    if (vite) stopVite(vite);
}

let failed = 0;
for (const r of results) {
    if (!r.ok) failed++;
    console.log(`${r.ok ? "ok  " : "FAIL"} ${r.theme}/${r.name}${r.ok ? "" : `  ${r.error}`}`);
    for (const problem of [...new Set(r.problems)]) console.log(`       console: ${problem}`);
}
console.log(`\n${results.length - failed}/${results.length} shots written to ${args.out}`);
process.exit(failed ? 1 : 0);
