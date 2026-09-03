import {expect} from '@playwright/test';

const LONG_UI_TIMEOUT = 15_000;
const METRICS_TIMEOUT = 240_000;
const POLL_INTERVALS = [5_000];

const UNIT = {B: 1, KiB: 1024, MiB: 1024 ** 2, GiB: 1024 ** 3, TiB: 1024 ** 4};

export function parseBytes(text) {
  const m = /^(-?[\d.]+)\s*(B|KiB|MiB|GiB|TiB)/.exec(String(text).trim());
  return m ? Number(m[1]) * UNIT[m[2]] : NaN;
}

export async function openMetricsPage(page) {
  await page.getByTestId('nav-metrics').click();
  await expect(page.getByTestId('metrics-page')).toBeVisible({timeout: LONG_UI_TIMEOUT});
  await ensureAllSpacesVisible(page);
}

async function ensureAllSpacesVisible(page) {
  const filter = page.getByTestId('metrics-space-filter');
  await expect(filter).toBeVisible();
  await filter.click();
  const rows = page.locator('[data-testid^="metrics-space-filter-row-"]');
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
  const select = page.getByTestId('metrics-deployment-select');
  if (!name) {
    await select.selectOption('');
    return;
  }
  let value = '';
  await expect.poll(async () => {
    const options = await select.locator('option').evaluateAll(nodes =>
      nodes.map(o => ({value: o.value, text: o.textContent || ''})));
    value = options.find(o => o.value && o.text.trim().endsWith(` / ${name}`))?.value || '';
    return value;
  }, {message: `expected metrics deployment option for ${name}`, timeout: LONG_UI_TIMEOUT}).not.toBe('');
  await select.selectOption(value);
}

async function refreshMetrics(page) {
  const button = page.getByTestId('metrics-refresh-button');
  await expect(button).toBeEnabled({timeout: LONG_UI_TIMEOUT});
  await button.click();
}

export async function expectMetricsOverviewRow(page, {name, minCpuCores = 0, minMemBytes = 0, maxAgeSeconds = 120}) {
  await openMetricsPage(page);
  await selectMetricsDeployment(page, '');
  await expect.poll(async () => {
    await refreshMetrics(page);
    const row = page.getByTestId(`metrics-overview-row-${name}`);
    if (await row.count() === 0) return 'row missing';
    const cells = await row.locator('td').allTextContents();
    const cpu = Number(cells[3]);
    const mem = parseBytes(cells[4]);
    const age = Number(String(cells[11]).replace(/s$/, ''));
    if (!(age <= maxAgeSeconds)) return `sample age ${cells[11]}`;
    if (!(cpu >= minCpuCores)) return `cpu ${cells[3]} < ${minCpuCores}`;
    if (!(mem >= minMemBytes)) return `memory ${cells[4]} < ${minMemBytes}`;
    return 'ok';
  }, {message: `expected metrics overview row for ${name}`, timeout: METRICS_TIMEOUT, intervals: POLL_INTERVALS}).toBe('ok');
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
  await expect(page.getByTestId('metrics-chart-cpu-svg').locator('path')).not.toHaveCount(0);
  await expect(page.getByTestId('metrics-chart-mem-svg').locator('path')).not.toHaveCount(0);
}

export async function expectMetricsControls(page, {name}) {
  await openMetricsPage(page);
  await selectMetricsDeployment(page, name);
  const split = page.getByTestId('metrics-split-toggle');
  await split.click();
  await expect(split).toHaveAttribute('aria-pressed', 'true');
  await expect.poll(() => page.evaluate(() => (window.__metricsResult?.charts.cpu?.series || []).length), {timeout: LONG_UI_TIMEOUT}).toBeGreaterThan(0);
  await split.click();
  await expect(split).toHaveAttribute('aria-pressed', 'false');

  await page.getByTestId('metrics-time-button').click();
  await page.getByRole('button', {name: 'Last 5 minutes'}).click();
  await expect.poll(async () => page.evaluate(() => {
    const r = window.__metricsResult;
    return r ? `${r.stepMs}:${r.buckets}` : '';
  }), {timeout: LONG_UI_TIMEOUT}).toMatch(/^10000:(3[01]|[12]\d|[1-9])$/);
  await expect(page.getByTestId('metrics-status')).toContainText('step 10 s');

  await page.getByTestId('metrics-time-button').click();
  await page.getByRole('button', {name: 'Last hour'}).click();
  await expect.poll(async () => page.evaluate(() => window.__metricsResult?.buckets || 0), {timeout: LONG_UI_TIMEOUT}).toBeGreaterThan(11);
}
