import {expect} from '@playwright/test';

const LONG_UI_TIMEOUT = 15_000;
const METRICS_TIMEOUT = 240_000;
const POLL_INTERVALS = [5_000];

const UNIT = {B: 1, KiB: 1024, MiB: 1024 ** 2, GiB: 1024 ** 3, TiB: 1024 ** 4};

export function parseBytes(text) {
  const m = /^(-?[\d.]+)\s*(B|KiB|MiB|GiB|TiB)/.exec(String(text).trim());
  return m ? Number(m[1]) * UNIT[m[2]] : NaN;
}

const overviewPanel = page => page.getByTestId('metrics-panel-overview');
const visiblePanel = page => page.locator('[data-testid^="metrics-panel-"]:visible');

export async function openMetricsPage(page) {
  await page.getByTestId('nav-metrics').click();
  await expect(page.getByTestId('metrics-page')).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await showMetricsOverview(page);
  await ensureAllSpacesVisible(page);
}

async function showMetricsOverview(page) {
  const tab = page.getByTestId('metrics-tab-overview');
  await tab.click();
  await expect(tab).toHaveAttribute('aria-selected', 'true');
  await expect(overviewPanel(page)).toBeVisible();
}

async function ensureAllSpacesVisible(page) {
  const filter = overviewPanel(page).getByTestId('metrics-space-filter');
  await expect(filter).toBeVisible();
  await filter.click();
  const rows = overviewPanel(page).locator('[data-testid^="metrics-space-filter-row-"]');
  await expect(rows.first()).toBeVisible({timeout: LONG_UI_TIMEOUT});
  const count = await rows.count();
  for (let i = 0; i < count; i += 1) {
    const row = rows.nth(i);
    if (await row.getAttribute('aria-checked') !== 'true') await row.click();
  }
  await page.mouse.click(2, 2);
  await expect(rows.first()).toBeHidden();
}

export async function selectMetricsDeployment(page, name) {
  await showMetricsOverview(page);
  if (!name) return;
  const row = overviewPanel(page).getByTestId(`metrics-overview-row-${name}`).first();
  await expect.poll(async () => {
    if (await row.count() > 0) return true;
    await refreshMetrics(page);
    return false;
  }, {message: `expected metrics overview row for ${name}`, timeout: LONG_UI_TIMEOUT}).toBe(true);
  const id = await row.getAttribute('data-deployment-id');
  await row.click();
  await expect(page.getByTestId(`metrics-tab-${id}`)).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByTestId(`metrics-panel-${id}`)).toBeVisible();
}

async function refreshMetrics(page) {
  const button = visiblePanel(page).getByTestId('metrics-refresh-button');
  await expect(button).toBeEnabled({timeout: LONG_UI_TIMEOUT});
  await button.click();
}

export async function expectMetricsOverviewRow(page, {name, minCpuCores = 0, minMemBytes = 0, maxAgeSeconds = 120}) {
  await openMetricsPage(page);
  await selectMetricsDeployment(page, '');
  await expect.poll(async () => {
    await refreshMetrics(page);
    const row = overviewPanel(page).getByTestId(`metrics-overview-row-${name}`);
    if (await row.count() === 0) return 'row missing';
    const cells = await row.locator('td').allTextContents();
    const cpu = Number(cells[3]);
    const mem = parseBytes(cells[4]);
    const ageText = cells[cells.length - 1];
    const age = Number(String(ageText).replace(/s$/, ''));
    if (!(age <= maxAgeSeconds)) return `sample age ${ageText}`;
    if (!(cpu >= minCpuCores)) return `cpu ${cells[3]} < ${minCpuCores}`;
    if (!(mem >= minMemBytes)) return `memory ${cells[4]} < ${minMemBytes}`;
    return 'ok';
  }, {message: `expected metrics overview row for ${name}`, timeout: METRICS_TIMEOUT, intervals: POLL_INTERVALS}).toBe('ok');
}

export async function expectMetricsVolume(page, {name, minBytes = 1}) {
  await openMetricsPage(page);
  await selectMetricsDeployment(page, '');
  await expect.poll(async () => {
    await refreshMetrics(page);
    const cell = overviewPanel(page).getByTestId(`metrics-overview-volume-${name}`);
    if (await cell.count() === 0) return 'row missing';
    const text = await cell.first().textContent();
    const bytes = parseBytes(text);
    if (!(bytes >= minBytes)) return `volume ${text} < ${minBytes}`;
    return 'ok';
  }, {message: `expected a measured volume for ${name} in the overview`, timeout: METRICS_TIMEOUT, intervals: POLL_INTERVALS}).toBe('ok');

  await selectMetricsDeployment(page, name);
  await expect.poll(async () => {
    await refreshMetrics(page);
    return page.evaluate((minBytes) => {
      const r = window.__metricsResult;
      if (!r) return 'no result';
      const values = (r.charts.volume?.series.find(s => s.label === 'used')?.values || []).filter(Number.isFinite);
      if (!values.length) return 'no volume series';
      const last = values[values.length - 1];
      if (!(last >= minBytes)) return `volume ${last} < ${minBytes}`;
      if (r.runs.some(run => run.label === 'v0 i0 r0')) return 'the volume series was counted as a run';
      return 'ok';
    }, minBytes);
  }, {message: `expected a charted volume for ${name}`, timeout: METRICS_TIMEOUT, intervals: POLL_INTERVALS}).toBe('ok');
  await expect(visiblePanel(page).getByTestId('metrics-chart-volume-svg').locator('path')).not.toHaveCount(0);
}

export async function expectMetricsCharts(page, {name, minCpuCores = 0, minMemBytes = 0, expectNetwork = false}) {
  await openMetricsPage(page);
  await selectMetricsDeployment(page, name);
  await expect.poll(async () => {
    await refreshMetrics(page);
    return page.evaluate(({minCpuCores, minMemBytes, expectNetwork}) => {
      const r = window.__metricsResult;
      if (!r) return 'no result';
      const series = (chart, label) => r.charts[chart]?.series.find(s => s.label === label);
      const last = (chart, label) => {
        const s = series(chart, label);
        if (!s) return NaN;
        for (let i = s.values.length - 1; i >= 0; i -= 1) if (Number.isFinite(s.values[i])) return s.values[i];
        return NaN;
      };
      const max = (chart, label) => {
        const vals = (series(chart, label)?.values || []).filter(Number.isFinite);
        return vals.length ? Math.max(...vals) : NaN;
      };
      if (!r.runs.length) return 'no runs';
      const cpu = last('cpu', 'total');
      if (!(cpu >= minCpuCores)) return `cpu ${cpu} < ${minCpuCores}`;
      const mem = last('mem', 'current');
      if (!(mem >= minMemBytes)) return `memory ${mem} < ${minMemBytes}`;
      if (!(last('pids', 'pids') >= 1)) return 'pids missing';
      if (expectNetwork && !(max('net', 'rx') > 0)) return `network rx ${max('net', 'rx')}`;
      return 'ok';
    }, {minCpuCores, minMemBytes, expectNetwork});
  }, {message: `expected charted metrics for ${name}`, timeout: METRICS_TIMEOUT, intervals: POLL_INTERVALS}).toBe('ok');
  await expect(visiblePanel(page).getByTestId('metrics-chart-cpu-svg').locator('path')).not.toHaveCount(0);
  await expect(visiblePanel(page).getByTestId('metrics-chart-mem-svg').locator('path')).not.toHaveCount(0);
}

export async function expectMetricsControls(page, {name}) {
  await openMetricsPage(page);
  await selectMetricsDeployment(page, name);
  const panel = visiblePanel(page);
  const split = panel.getByTestId('metrics-split-toggle');
  await split.click();
  await expect(split).toHaveAttribute('aria-pressed', 'true');
  await expect.poll(() => page.evaluate(() => (window.__metricsResult?.charts.cpu?.series || []).length), {timeout: LONG_UI_TIMEOUT}).toBeGreaterThan(0);
  await split.click();
  await expect(split).toHaveAttribute('aria-pressed', 'false');

  await panel.getByTestId('metrics-time-button').click();
  await panel.getByRole('button', {name: 'Last 5 minutes'}).click();
  await expect.poll(async () => page.evaluate(() => {
    const r = window.__metricsResult;
    return r ? `${r.stepMs}:${r.buckets}` : '';
  }), {timeout: LONG_UI_TIMEOUT}).toMatch(/^10000:(3[01]|[12]\d|[1-9])$/);
  await expect(panel.getByTestId('metrics-status')).toContainText('step 10 s');

  await panel.getByTestId('metrics-time-button').click();
  await panel.getByRole('button', {name: 'Last hour'}).click();
  await expect.poll(async () => page.evaluate(() => window.__metricsResult?.buckets || 0), {timeout: LONG_UI_TIMEOUT}).toBeGreaterThan(11);
}

const nodesPanel = page => page.getByTestId('metrics-panel-nodes');

function clusterNodeRow(page, name) {
  const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return page.locator('[data-testid^="machine-row-"]')
    .filter({has: page.locator('[data-testid="node-name"]', {hasText: new RegExp(`^${escaped}$`)})});
}

async function showNodesOverview(page) {
  await page.getByTestId('nav-metrics').click();
  await expect(page.getByTestId('metrics-page')).toBeVisible({timeout: LONG_UI_TIMEOUT});
  const tab = page.getByTestId('metrics-tab-nodes');
  await tab.click();
  await expect(tab).toHaveAttribute('aria-selected', 'true');
  await expect(nodesPanel(page)).toBeVisible();
}

export async function expectNodeMetricsOverviewRow(page, {name, minCpuCores = 0, minMemBytes = 0, maxAgeSeconds = 120}) {
  await showNodesOverview(page);
  await expect.poll(async () => {
    await refreshMetrics(page);
    const row = nodesPanel(page).getByTestId(`metrics-nodes-row-${name}`);
    if (await row.count() === 0) return 'row missing';
    const cells = await row.locator('td').allTextContents();
    const cpu = Number(cells[1]);
    const mem = parseBytes(cells[4]);
    const memPct = parseFloat(cells[5]);
    const disk = parseBytes(cells[6]);
    const diskPct = parseFloat(cells[7]);
    const age = Number(String(cells[12]).replace(/s$/, ''));
    if (!(age <= maxAgeSeconds)) return `sample age ${cells[12]}`;
    if (!(cpu >= minCpuCores)) return `cpu ${cells[1]} < ${minCpuCores}`;
    if (!(mem >= minMemBytes)) return `memory ${cells[4]} < ${minMemBytes}`;
    if (!(memPct > 0 && memPct <= 100)) return `memory percent ${cells[5]}`;
    if (!(disk > 0)) return `disk used ${cells[6]}`;
    if (!(diskPct > 0 && diskPct <= 100)) return `disk percent ${cells[7]}`;
    return 'ok';
  }, {message: `expected nodes overview row for ${name}`, timeout: METRICS_TIMEOUT, intervals: POLL_INTERVALS}).toBe('ok');
}

export async function expectNodeMetricsCharts(page, {name, minCpuCores = 0}) {
  await showNodesOverview(page);
  const row = nodesPanel(page).getByTestId(`metrics-nodes-row-${name}`);
  await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
  const id = await row.getAttribute('data-node-id');
  await row.click();
  await expect(page.getByTestId(`metrics-tab-node-${id}`)).toHaveAttribute('aria-selected', 'true');
  const panel = page.getByTestId(`metrics-panel-node-${id}`);
  await expect(panel).toBeVisible();
  await expect.poll(async () => {
    await refreshMetrics(page);
    return page.evaluate(({id, minCpuCores}) => {
      const r = window.__metricsNodeResult;
      if (!r || String(r.nodeId) !== id) return 'no result';
      const last = (chart, label) => {
        const s = r.charts[chart]?.series.find(x => x.label === label);
        if (!s) return NaN;
        for (let i = s.values.length - 1; i >= 0; i -= 1) if (Number.isFinite(s.values[i])) return s.values[i];
        return NaN;
      };
      const cpu = last('cpu', 'total');
      if (!(cpu >= minCpuCores && cpu > 0)) return `cpu ${cpu} < ${minCpuCores}`;
      if (!(last('load', 'cpus') >= 1)) return `cpu count ${last('load', 'cpus')}`;
      if (!(last('mem', 'used') > 0 && last('mem', 'total') >= last('mem', 'used'))) return `memory ${last('mem', 'used')} of ${last('mem', 'total')}`;
      if (!(last('disk', 'used') > 0 && last('disk', 'total') >= last('disk', 'used'))) return `disk ${last('disk', 'used')} of ${last('disk', 'total')}`;
      if (!(last('net', 'rx') >= 0)) return 'network rx missing';
      if (!(last('io', 'write') >= 0)) return 'disk write missing';
      return 'ok';
    }, {id, minCpuCores});
  }, {message: `expected charted node metrics for ${name}`, timeout: METRICS_TIMEOUT, intervals: POLL_INTERVALS}).toBe('ok');
  await expect(panel.getByTestId('metrics-node-chart-cpu-svg').locator('path')).not.toHaveCount(0);
  await expect(panel.getByTestId('metrics-node-chart-disk-svg').locator('path')).not.toHaveCount(0);

  await panel.getByTestId('metrics-time-button').click();
  await panel.getByRole('button', {name: 'Last 5 minutes'}).click();
  await expect.poll(async () => page.evaluate(() => window.__metricsNodeResult?.stepMs || 0), {timeout: LONG_UI_TIMEOUT}).toBe(10_000);
  await expect(panel.getByTestId('metrics-status')).toContainText('step 10 s');

  await page.getByTestId(`metrics-tab-node-${id}`).getByRole('button', {name: `Close ${name}`}).click();
  await expect(page.getByTestId(`metrics-tab-node-${id}`)).toHaveCount(0);
  await expect(panel).toHaveCount(0);
}

export async function expectNodeDiskColumn(page, {names}) {
  await expect.poll(async () => {
    await page.getByTestId('nav-metrics').click();
    await page.getByTestId('nav-cluster').click();
    await page.getByTestId('nodes-tab-nodes').click();
    for (const name of names) {
      const row = clusterNodeRow(page, name);
      if (await row.count() !== 1) return `row for ${name} missing`;
      const cell = row.getByTestId('node-disk');
      try {
        await expect(cell).not.toHaveAttribute('data-state', 'loading', {timeout: LONG_UI_TIMEOUT});
      } catch {
        return `disk usage for ${name} still loading`;
      }
      const state = await cell.getAttribute('data-state');
      const text = (await cell.textContent() || '').trim();
      if (state !== 'loaded') return `disk usage for ${name} is ${state}`;
      if (!/^\d+% of [\d.]+ (MiB|GiB|TiB)$/.test(text)) return `disk usage for ${name} reads ${text}`;
    }
    return 'ok';
  }, {message: `expected disk usage for ${names.join(', ')} on the Nodes page`, timeout: METRICS_TIMEOUT, intervals: POLL_INTERVALS}).toBe('ok');

  const name = names[names.length - 1];
  const row = clusterNodeRow(page, name);
  await row.click();
  const inspector = page.getByTestId('node-inspector');
  await expect(inspector.getByTestId('node-inspector-disk-data')).toContainText(/\d+% · .+ used of .+ · .+ available/, {timeout: LONG_UI_TIMEOUT});
  await expect(row.getByTestId('node-disk')).toHaveText(/^\d+%$/);
  await inspector.getByRole('button', {name: 'Close details'}).click();
  await expect(inspector).toBeHidden();
  await expect(row.getByTestId('node-disk')).toHaveText(/^\d+% of /);
}
