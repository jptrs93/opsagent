import {expect, test} from '@playwright/test';
import {signInWithPassword} from '../helpers/ui.js';

const LONG_UI_TIMEOUT = 15_000;
const INVITEE = 'E2E Invitee';
const INVITEE_PASSWORD = 'e2e-invitee-password';

function userRow(page, userName) {
  return page.locator('[data-testid^="user-row-"]').filter({hasText: userName});
}

async function newContextPage(ctx) {
  const browser = ctx.page.context().browser();
  const context = await browser.newContext({
    baseURL: process.env.OPD_BASE_URL || 'http://localhost:8080',
    ignoreHTTPSErrors: process.env.OPD_IGNORE_HTTPS_ERRORS === 'true',
  });
  const page = await context.newPage();
  page.setDefaultTimeout(5_000);
  page.setDefaultNavigationTimeout(10_000);
  return {context, page};
}

// openEnrolOverlay opens Enrol or recover from the Users page and returns
// the overlay.
async function openEnrolOverlay(page) {
  await page.getByTestId('nav-users').click();
  await page.getByTestId('new-user-button').click();
  const overlay = page.getByTestId('enrol-overlay');
  await expect(overlay).toBeVisible({timeout: LONG_UI_TIMEOUT});
  return overlay;
}

// The token in a link is l_<session id>.<secret>; the Sessions page keys
// its rows by that session id.
function linkSessionId(url) {
  const token = url.split('/enrol/')[1] || '';
  return token.slice(2).split('.')[0];
}

async function readLink(overlay) {
  const link = overlay.getByTestId('enrol-link');
  await expect(link).toBeVisible({timeout: LONG_UI_TIMEOUT});
  const url = (await link.textContent())?.trim();
  if (!url) throw new Error('enrol link is empty');
  await overlay.getByTestId('enrol-done').click();
  await expect(overlay).toBeHidden({timeout: LONG_UI_TIMEOUT});
  return url;
}

async function expectLinkRefused(ctx, url) {
  const {context, page} = await newContextPage(ctx);
  try {
    await page.goto(url);
    await expect(page.getByTestId('enrol-landing-error')).toBeVisible({timeout: LONG_UI_TIMEOUT});
    await expect(page.getByTestId('landing-password-input')).toHaveCount(0);
  } finally {
    await context.close();
  }
}

export const enrolLinkCases = [
  {
    id: 'enrol-link-issued',
    title: 'issue a sign-in link for a new user',
    description: 'The admin enrols a user from the Users page with no password and no privileges, and takes the one-time sign-in link; the user shows as pending.',
    requires: ['password-login'],
    async run(ctx) {
      const overlay = await openEnrolOverlay(ctx.page);
      await overlay.getByTestId('enrol-choose-new').click();
      await overlay.getByTestId('enrol-name-input').fill(INVITEE);
      await overlay.getByTestId('enrol-create-link').click();
      ctx.enrolLink = await readLink(overlay);
      const row = userRow(ctx.page, INVITEE);
      await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
      await expect(row.getByText('pending', {exact: true})).toBeVisible();
    },
  },
  {
    id: 'enrol-link-redeemed',
    title: 'redeem the link and finish setting up',
    description: 'A fresh browser opens the link, is signed in, sets a password, skips the passkey offer, and lands on the dashboard; the admin sees the user activated.',
    requires: ['enrol-link-issued'],
    async run(ctx) {
      const {context, page} = await newContextPage(ctx);
      ctx.invitee = {context, page};
      await page.goto(ctx.enrolLink);
      await test.step('the landing page asks for a password', async () => {
        await expect(page.getByTestId('landing-password-input')).toBeVisible({timeout: LONG_UI_TIMEOUT});
        await page.getByTestId('landing-password-input').fill(INVITEE_PASSWORD);
        await page.getByTestId('landing-password-continue').click();
      });
      await test.step('the passkey offer can be skipped', async () => {
        const skip = page.getByTestId('landing-skip');
        const signOut = page.getByTestId('nav-sign-out');
        await expect(skip.or(signOut).first()).toBeVisible({timeout: LONG_UI_TIMEOUT});
        if (await skip.isVisible()) await skip.click();
        await expect(signOut).toBeVisible({timeout: LONG_UI_TIMEOUT});
      });
      await test.step('the admin sees the user activated', async () => {
        const row = userRow(ctx.page, INVITEE);
        await expect(row.getByText('pending', {exact: true})).toHaveCount(0, {timeout: LONG_UI_TIMEOUT});
      });
    },
  },
  {
    id: 'enrol-link-single-use',
    title: 'refuse the link a second time',
    description: 'Opening the redeemed link again is refused and offers the login page instead.',
    requires: ['enrol-link-redeemed'],
    async run(ctx) {
      await expectLinkRefused(ctx, ctx.enrolLink);
    },
  },
  {
    id: 'enrol-invitee-password-login',
    title: 'the invitee signs in with the password they chose',
    description: 'The invitee signs out and back in with the username the admin set and the password chosen on the landing page.',
    requires: ['enrol-link-redeemed'],
    async run(ctx) {
      const page = ctx.invitee.page;
      await page.getByTestId('nav-sign-out').click();
      await signInWithPassword(page, {username: INVITEE, password: INVITEE_PASSWORD});
      await expect(page.getByTestId('nav-sign-out')).toBeVisible({timeout: LONG_UI_TIMEOUT});
    },
  },
  {
    id: 'enrol-link-cancelled',
    title: 'cancel a recovery link before it is used',
    description: 'The admin issues a recovery link for the invitee, cancels it from the Sessions page, and the link is refused.',
    requires: ['enrol-invitee-password-login'],
    async run(ctx) {
      const overlay = await openEnrolOverlay(ctx.page);
      await overlay.getByTestId('enrol-choose-recover').click();
      await overlay.locator('[data-testid^="enrol-recover-user-"]').filter({hasText: INVITEE}).first().click();
      await expect(overlay.getByTestId('enrol-recover-summary')).toBeVisible();
      await overlay.getByTestId('enrol-recover-link').click();
      const url = await readLink(overlay);

      await ctx.page.getByTestId('nav-sessions').click();
      await ctx.page.getByRole('tab', {name: /User sessions/}).click();
      const row = ctx.page.getByTestId(`user-session-link-${linkSessionId(url)}`);
      await expect(row).toBeVisible({timeout: LONG_UI_TIMEOUT});
      await expect(row.getByText('Unused', {exact: true})).toBeVisible();
      await expect(row).toContainText(INVITEE);
      await row.getByRole('button', {name: 'Cancel this link', exact: true}).click();
      await expect(row.getByText('Cancelled', {exact: true})).toBeVisible({timeout: LONG_UI_TIMEOUT});

      await expectLinkRefused(ctx, url);
      await ctx.invitee.context.close();
      ctx.invitee = null;
      await ctx.page.getByTestId('nav-status').click();
    },
  },
];
