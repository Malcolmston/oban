import { test, expect, type Page } from '@playwright/test';

// Every hash-routed tab id in the site's nav, in order.
const TAB_IDS = [
  'overview',
  'releases',
  'docs',
] as const;

// Network to these hosts is blocked in the sandbox; failures there are expected
// and must not fail the page-error assertion.
const IGNORED_HOSTS = ['kit.fontawesome.com', 'api.github.com'];

// Collected uncaught page errors, checked after every test.
let pageErrors: string[] = [];

test.beforeEach(async ({ page }) => {
  pageErrors = [];
  page.on('pageerror', (err) => {
    const msg = `${err.name}: ${err.message}\n${err.stack ?? ''}`;
    if (!IGNORED_HOSTS.some((h) => msg.includes(h))) pageErrors.push(msg);
  });
  // Reduced motion so the wormhole page transition resolves instantly.
  await page.emulateMedia({ reducedMotion: 'reduce' });
});

test.afterEach(() => {
  expect(pageErrors, `unexpected page errors:\n${pageErrors.join('\n---\n')}`).toEqual([]);
});

async function gotoTab(page: Page, id: string) {
  await page.goto(`#${id}`);
  await expect(page.locator('.view.active')).toHaveAttribute('id', `view-${id}`);
}

// Activate a nav tab link. Across 200+ device profiles the tab bar renders three
// ways — inline (desktop), an overflow-scrolled strip (narrow tablets), and a
// collapsed dropdown behind a menu button that overlays the sticky header
// (phones) — so a coordinate-based click is not reliably actionable everywhere.
// Dispatching the click event exercises the same React onClick -> hash-routing
// handler on every layout; genuine pointer-click coverage lives in the
// "internal tab links actually navigate" test below.
async function activateTab(page: Page, id: string) {
  await page.locator(`nav.tabs a.tab[href="#${id}"]`).dispatchEvent('click');
}

test.describe('every page renders responsively', () => {
  for (const id of TAB_IDS) {
    test(`#${id}: single active view, visible heading, no horizontal overflow`, async ({ page }) => {
      await gotoTab(page, id);

      // Exactly one active view.
      await expect(page.locator('.view.active')).toHaveCount(1);

      // A visible heading in the active view.
      await expect(page.locator('.view.active :is(h1, h2, h3)').first()).toBeVisible();

      // Per-device responsive guarantee: no horizontal overflow.
      const { scrollWidth, innerWidth } = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        innerWidth: window.innerWidth,
      }));
      expect(scrollWidth, `#${id} overflows: scrollWidth ${scrollWidth} > innerWidth ${innerWidth}`)
        .toBeLessThanOrEqual(innerWidth + 2);
    });
  }
});

test('nav tabs switch the active view and update the hash', async ({ page }) => {
  await gotoTab(page, 'overview');
  for (const id of TAB_IDS) {
    await activateTab(page, id);
    await expect(page.locator('.view.active')).toHaveAttribute('id', `view-${id}`);
    await expect(page).toHaveURL(new RegExp(`#${id}$`));
  }
});

test('every link is valid (internal targets exist, external links are safe)', async ({ page }) => {
  for (const id of TAB_IDS) {
    await gotoTab(page, id);
    const anchors = page.locator('a[href]');
    const count = await anchors.count();
    expect(count, `#${id} should have links`).toBeGreaterThan(0);

    for (let i = 0; i < count; i++) {
      const a = anchors.nth(i);
      const href = (await a.getAttribute('href')) ?? '';

      // No dead hrefs.
      expect(href, `#${id} link ${i} has an empty href`).not.toBe('');
      expect(href, `#${id} link ${i} has a bare "#" href`).not.toBe('#');

      if (/^https?:\/\//.test(href)) {
        // External: valid absolute URL, opens in a new tab, safe rel.
        expect(() => new URL(href), `invalid external URL: ${href}`).not.toThrow();
        await expect(a, `external link ${href} must open in a new tab`).toHaveAttribute('target', '_blank');
        const rel = (await a.getAttribute('rel')) ?? '';
        expect(rel, `external link ${href} must have rel*=noopener`).toContain('noopener');
      } else if (href.startsWith('#')) {
        // Internal: must map to a known tab, a DocsApp package route
        // (#pkg/<importPath>, hash-routed by the React reference), or an
        // in-page element id. getElementById is used rather than a CSS locator
        // because symbol/route hashes contain "/" and "." (invalid selectors).
        const target = href.slice(1);
        const isTab = (TAB_IDS as readonly string[]).includes(target);
        const isDocsRoute = target.startsWith('pkg/');
        let ok = isTab || isDocsRoute;
        if (!ok) ok = await page.evaluate((t) => !!document.getElementById(t), target);
        expect(ok, `#${id}: internal link "${href}" maps to nothing`).toBeTruthy();
      } else if (href === './api/') {
        // The generated API reference, published alongside this site under
        // /oban/api/. It is produced by the Go doc generator at deploy time,
        // not by `vite build`, so it does not exist in the preview server used
        // here — we assert the link is well-formed rather than navigating to it.
        const resolved = new URL(href, page.url());
        expect(resolved.pathname, `docs link should resolve under /oban/`).toBe('/oban/api/');
      } else {
        throw new Error(`#${id}: unexpected non-hash, non-http href "${href}"`);
      }
    }
  }
});

test('docs tab renders the React API reference with a package listed', async ({ page }) => {
  await gotoTab(page, 'docs');
  // DocsApp fetches doc.json and renders a package sidebar; assert the actual
  // rendered reference (at least one package + a package view), not just a link.
  // oban is a single-package module (package oban), so at least one package link is expected.
  const pkgLinks = page.locator('#view-docs .docs-nav .docs-pkg-link');
  await expect(pkgLinks.first()).toBeVisible();
  expect(await pkgLinks.count(), 'expected at least one package in the reference').toBeGreaterThanOrEqual(1);
  await expect(page.locator('#view-docs .pkg-view .pkg-title').first()).toBeVisible();
});

test('internal tab links actually navigate to their view', async ({ page }) => {
  await gotoTab(page, 'overview');
  // The overview hero's "API docs" pill links to the #docs tab.
  await page.locator('.view.active a[href="#docs"]').first().click();
  await expect(page.locator('.view.active')).toHaveAttribute('id', 'view-docs');
  await expect(page).toHaveURL(/#docs$/);
});
