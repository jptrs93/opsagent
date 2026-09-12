import { test as base, expect, type Page } from '@playwright/test';
import { logEventVariants } from '../src/lib/log-event';

const articles = [
  {
    slug: 'networking', title: 'Networking', editors: 5, figures: 2,
    anchors: ['goals', 'addressing', 'dns', 'balancing', 'routing', 'rollover', 'policy', 'egress', 'ingress'],
    section: 'DNS', hash: 'dns', content: 'private cluster network',
  },
  {
    slug: 'logging', title: 'Logging', editors: 6, figures: 0,
    anchors: ['goals', 'structure', 'storage', 'collector', 'search', 'system', 'capture'],
    section: 'Log collector', hash: 'collector', content: 'built-in logging system',
  },
] as const;

const test = base.extend<{ browserErrors: void }>({
  browserErrors: [async ({ page }, use) => {
    const errors: string[] = [];
    page.on('pageerror', (error) => errors.push(error.stack ?? error.message));
    page.on('console', (message) => {
      if (message.type() === 'error') errors.push(message.text());
    });
    await use();
    expect(errors, 'Browser JavaScript and console errors').toEqual([]);
  }, { auto: true }],
});

async function assertDocument(page: Page) {
  const problems = await page.evaluate(() => {
    const ids = Array.from(document.querySelectorAll('[id]'), (element) => element.id);
    const brokenAnchors = Array.from(document.querySelectorAll<HTMLAnchorElement>('a[href]'))
      .filter((link) => link.origin === location.origin && link.pathname === location.pathname && link.hash)
      .filter((link) => !document.getElementById(decodeURIComponent(link.hash.slice(1))))
      .map((link) => link.getAttribute('href'));
    return { duplicateIds: ids.filter((id, index) => ids.indexOf(id) !== index), brokenAnchors };
  });
  expect(problems).toEqual({ duplicateIds: [], brokenAnchors: [] });
  expect(await page.evaluate(() => Math.max(document.body.scrollWidth, document.documentElement.scrollWidth)
    - document.documentElement.clientWidth), 'No horizontal page overflow').toBeLessThanOrEqual(1);
}

async function assertEditors(page: Page, count: number) {
  await expect(page.locator('.cm-editor')).toHaveCount(count);
  await expect(page.locator('.code-block')).toHaveCount(count);
  await expect(page.locator('.code-block pre')).toHaveCount(0);
  expect(await page.locator('.editor-host').evaluateAll((hosts) =>
    hosts.map((host) => host.querySelectorAll('.cm-editor').length))).toEqual(Array(count).fill(1));
}

for (const article of articles) {
  test(`${article.slug}: content, diagrams, editors, anchors and responsive navigation`, async ({ page }) => {
    const response = await page.goto(`/${article.slug}/`);
    expect(response?.status()).toBe(200);
    await expect(page).toHaveTitle(new RegExp(`${article.title}.*OpenDeploy Docs`));
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(article.title);
    await expect(page.getByRole('main')).toContainText(article.content);
    await expect(page.locator('figure')).toHaveCount(article.figures);
    await expect(page.locator('figure svg[role="img"][aria-label]')).toHaveCount(article.figures);
    for (const figure of await page.locator('figure').all()) {
      await expect(figure).toBeVisible();
      await expect(figure.locator('figcaption')).not.toBeEmpty();
    }
    await assertEditors(page, article.editors);
    await assertDocument(page);

    const menu = page.getByRole('button', { name: 'Menu', exact: true });
    if (page.viewportSize()!.width < 900) {
      await expect(page.locator('#sidebar')).toBeHidden();
      await menu.click();
      await expect(menu).toHaveAttribute('aria-expanded', 'true');
      await expect(page.locator('#sidebar')).toBeVisible();
      await page.getByRole('navigation', { name: 'Documentation', exact: true })
        .getByRole('link', { name: article.title, exact: true }).focus();
      await page.keyboard.press('Escape');
      await expect(menu).toHaveAttribute('aria-expanded', 'false');
      await expect(menu).toBeFocused();
      await expect(page.locator('#sidebar')).toBeHidden();
      await menu.click();
    } else {
      await expect(menu).toBeHidden();
    }
    const sectionLink = page.locator('#page-nav').getByRole('link', { name: article.section, exact: true, includeHidden: true });
    await sectionLink.click();
    await expect(page).toHaveURL(new RegExp(`/${article.slug}/#${article.hash}$`));
    await expect(page.locator(`#${article.hash}`)).toBeInViewport();
    await expect(sectionLink).toHaveAttribute('aria-current', 'location');
    if (page.viewportSize()!.width < 900) {
      await expect(menu).toHaveAttribute('aria-expanded', 'false');
      await expect(page.locator('#sidebar')).toBeHidden();
    }
    await assertDocument(page);
  });
}

test('schema tabs change highlighted, read-only source and copy the active schema', async ({ page }) => {
  const copied: string[] = [];
  let failCopy = false;
  await page.exposeFunction('copyForTest', (text: string) => {
    if (failCopy) throw new Error('Clipboard denied for test');
    copied.push(text);
  });
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: (text: string) => (window as unknown as {
        copyForTest: (text: string) => Promise<void>;
      }).copyForTest(text) },
    });
  });
  await page.goto('/logging/');
  await assertEditors(page, 6);
  const tabs = page.getByRole('tablist', { name: 'LogEvent representation' });
  const panel = page.getByRole('tabpanel');
  await expect(tabs.getByRole('tab')).toHaveText(['Protobuf', 'Smithy', 'TypeScript']);
  await expect(tabs.getByRole('tab', { name: 'Protobuf', exact: true })).toHaveAttribute('aria-selected', 'true');

  for (const variant of logEventVariants) {
    await tabs.getByRole('tab', { name: variant.label, exact: true }).click();
    const source = panel.getByRole('textbox', { name: `LogEvent - ${variant.label} source`, exact: true });
    await expect(source).toContainText(variant.code.split('\n')[0]);
    // Inspect rendered CodeMirror tokens, not the fallback <pre> or tab label.
    await expect.poll(() => source.locator('.cm-line span').evaluateAll((tokens) =>
      tokens.some((token) => getComputedStyle(token).color !== getComputedStyle(token.parentElement!).color),
    )).toBe(true);
    await expect(source).toHaveAttribute('aria-readonly', 'true');
    await source.focus();
    expect(await source.evaluate((element) => ({
      outline: getComputedStyle(element).outlineStyle,
      shadow: getComputedStyle(element.closest('.code-body')!).boxShadow,
    }))).toEqual({ outline: 'none', shadow: 'none' });
    const before = await source.innerText();
    await page.keyboard.type('SHOULD_NOT_CHANGE');
    await page.keyboard.press('Backspace');
    await expect(source).toHaveText(before, { useInnerText: true });
    await panel.getByRole('button', { name: `Copy LogEvent - ${variant.label} source`, exact: true }).click();
    await expect(panel.getByRole('status')).toHaveText('Copied');
    expect(copied.at(-1)).toBe(variant.code);
    await assertEditors(page, 6);
    await assertDocument(page);
  }

  for (const [key, label] of [
    ['Home', 'Protobuf'], ['ArrowRight', 'Smithy'], ['End', 'TypeScript'],
    ['ArrowRight', 'Protobuf'], ['ArrowLeft', 'TypeScript'], ['Home', 'Protobuf'],
  ]) {
    await tabs.locator('[aria-selected="true"]').focus();
    await page.keyboard.press(key);
    const selected = tabs.getByRole('tab', { name: label, exact: true });
    await expect(selected).toBeFocused();
    await expect(selected).toHaveAttribute('aria-selected', 'true');
    await expect(selected).toHaveAttribute('tabindex', '0');
    await expect(tabs.locator('[tabindex="0"]')).toHaveCount(1);
    await expect(tabs.locator('[aria-selected="false"][tabindex="-1"]')).toHaveCount(2);
    await expect(panel).toHaveAttribute('aria-labelledby', (await selected.getAttribute('id'))!);
    await expect(panel.getByRole('textbox')).toContainText(logEventVariants.find((variant) => variant.label === label)!.code.split('\n')[0]);
    await expect(panel.getByRole('status')).toBeEmpty();
  }
  failCopy = true;
  await panel.getByRole('button', { name: 'Copy LogEvent - Protobuf source', exact: true }).click();
  await expect(panel.getByRole('status')).toHaveText('Copy failed. Select and copy the code.');
  expect(copied).toHaveLength(3);
});

test('client navigation replaces sidebar anchors and remounts editors without duplicates', async ({ page }) => {
  await page.goto('/logging/');
  await assertEditors(page, 6);
  await page.getByRole('tab', { name: 'Smithy', exact: true }).click();
  await page.evaluate(() => { document.documentElement.dataset.navigationTest = 'same-document'; });

  for (const article of articles) {
    const previousEditors = await page.locator('.cm-editor').elementHandles();
    const previousSidebar = await page.locator('#page-nav').elementHandle();
    if (page.viewportSize()!.width < 900) await page.getByRole('button', { name: 'Menu', exact: true }).click();
    await page.getByRole('navigation', { name: 'Documentation', exact: true })
      .getByRole('link', { name: article.title, exact: true }).click();
    await expect(page).toHaveURL(`/${article.slug}/`);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(article.title);
    await expect(page.locator('html')).toHaveAttribute('data-navigation-test', 'same-document');
    await assertEditors(page, article.editors);
    expect(await page.locator('#page-nav a').evaluateAll((links) => links.map((link) => link.getAttribute('href'))))
      .toEqual(article.anchors.map((id) => `#${id}`));
    for (const editor of previousEditors) expect(await editor.evaluate((element) => element.isConnected)).toBe(false);
    expect(await previousSidebar!.evaluate((element) => element.isConnected)).toBe(false);
    await assertDocument(page);
  }
  await expect(page.getByRole('tab', { name: 'Protobuf', exact: true })).toHaveAttribute('aria-selected', 'true');
});

test('legacy static URLs preserve query/hash and the data-model reference loads', async ({ page }) => {
  for (const [legacy, destination, heading, target] of [
    ['/logging.html?test=1#capture', '/logging/?test=1#capture', 'Logging', 'capture'],
    ['/networking.html#dns', '/networking/#dns', 'Networking', 'dns'],
  ]) {
    await page.goto(legacy);
    await expect(page).toHaveURL(destination);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(heading);
    await expect(page.locator(`#${target}`)).toBeInViewport();
  }
  const response = await page.goto('/data-model/index.html');
  expect(response?.status()).toBe(200);
  await expect(page).toHaveTitle(/OpenDeploy.*Data model/);
  await expect(page.locator('.topbar')).toContainText('Data model');
  await expect(page.locator('#canvas')).toBeVisible();
  await expect(page.locator('#canvas .gv-schema').first()).toBeVisible();
  await expect(page.getByRole('button', { name: 'Zoom in', exact: true })).toBeVisible();
});

test.describe('without JavaScript', () => {
  test.use({ javaScriptEnabled: false });

  for (const article of articles) {
    test(`${article.slug} retains article headings and readable source`, async ({ page }) => {
      const response = await page.goto(`/${article.slug}/`);
      expect(response?.status()).toBe(200);
      await expect(page.getByRole('heading', { level: 1 })).toHaveText(article.title);
      await expect(page.getByRole('main')).toContainText(article.content);
      for (const id of article.anchors) await expect(page.locator(`#${id}`)).toBeVisible();
      await expect(page.locator('#page-nav a')).toHaveCount(article.anchors.length);
      await expect(page.locator('#page-nav')).toBeVisible();
      await expect(page.locator('pre code')).toHaveCount(article.editors);
      for (const code of await page.locator('pre code').all()) {
        await expect(code).toBeVisible();
        await expect(code).not.toBeEmpty();
      }
      await expect(page.locator('.cm-editor')).toHaveCount(0);
      await expect(page.locator('figure')).toHaveCount(article.figures);
      if (article.slug === 'logging') {
        const event = JSON.parse((await page.locator('pre code.language-json').textContent())!);
        expect(typeof event.source.deployment_id).toBe('number');
        expect(JSON.parse(event.payload)).toEqual({
          ...event.parsed_payload.ints,
          ...event.parsed_payload.floats,
          ...event.parsed_payload.strings,
        });
        await expect(page.getByRole('tab', { name: 'Protobuf', exact: true })).toHaveAttribute('aria-selected', 'true');
        expect(await page.getByRole('tabpanel').locator('pre code.language-protobuf').textContent()).toBe(logEventVariants[0].code);
      }
      await assertDocument(page);
    });
  }
});
