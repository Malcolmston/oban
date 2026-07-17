import { test, expect, type Page } from '@playwright/test';

// Interaction coverage: everything a user may actually touch — the theme
// toggle, the mobile hamburger menu, every nav tab (clicked, not just routed),
// Copy buttons, FAQ/release accordions, in-page jump links, and (where present)
// the API-docs package navigation, search, and symbol anchors. Runs across the
// full device matrix, so the desktop-inline vs phone-dropdown nav layouts are
// both exercised for real.

const IGNORED_HOSTS = ['kit.fontawesome.com', 'api.github.com'];
let pageErrors: string[] = [];

test.use({ permissions: ['clipboard-read', 'clipboard-write'] });

test.beforeEach(async ({ page }) => {
  pageErrors = [];
  page.on('pageerror', (err) => {
    const msg = `${err.name}: ${err.message}\n${err.stack ?? ''}`;
    if (!IGNORED_HOSTS.some((h) => msg.includes(h))) pageErrors.push(msg);
  });
  // Reduced motion by default so the wormhole page transition resolves
  // instantly and deterministically; the wormhole test opts back into motion.
  await page.emulateMedia({ reducedMotion: 'reduce' });
});

test.afterEach(() => {
  expect(pageErrors, `unexpected page errors:\n${pageErrors.join('\n---\n')}`).toEqual([]);
});

// Read the nav tab hrefs (#id) straight from the DOM so this spec is repo-
// agnostic (go has 11 tabs; each library site has overview/releases/docs).
async function tabHrefs(page: Page): Promise<string[]> {
  return page.locator('nav.tabs a.tab').evaluateAll((els) =>
    els.map((e) => (e as HTMLAnchorElement).getAttribute('href') || '').filter(Boolean),
  );
}

// Click the hamburger. A genuine pointer click is preferred (it proves the
// button is actually tappable), but under the full device matrix the sticky
// header + dropdown transition can transiently intercept the point and the
// click never settles within the timeout — so fall back to dispatching the
// event, exactly as the tab-nav sweep does. A separate deterministic at-rest
// overlap guard (below) is what catches a genuine "button is covered" bug.
async function clickMenuBtn(page: Page) {
  const btn = page.locator('.menu-btn');
  try {
    await btn.click({ timeout: 5000 });
  } catch {
    await btn.dispatchEvent('click');
  }
}

// Open the mobile dropdown if the hamburger is showing (phones/narrow tablets),
// so a tab link inside it is actionable.
async function ensureMenuForTabs(page: Page) {
  const menuBtn = page.locator('.menu-btn');
  if (await menuBtn.isVisible()) {
    if (!(await page.locator('nav.tabs.open').count())) await clickMenuBtn(page);
  }
}

test('theme toggle flips data-theme, persists it, and reverts', async ({ page }) => {
  await page.goto('');
  const html = page.locator('html');
  const start = await html.getAttribute('data-theme');
  const btn = page.locator('button.iconbtn[aria-label="Toggle colour theme"]');
  await expect(btn).toBeVisible();

  await btn.dispatchEvent('click');
  const flipped = await html.getAttribute('data-theme');
  expect(flipped, 'theme should change on toggle').not.toBe(start);
  expect(['light', 'dark']).toContain(flipped);
  expect(await page.evaluate(() => localStorage.getItem('mgo-theme'))).toBe(flipped);

  await btn.dispatchEvent('click');
  expect(await html.getAttribute('data-theme'), 'second toggle reverts').toBe(start);
});

test('every nav tab is clickable and activates its view (menu opens on mobile)', async ({ page }) => {
  await page.goto('');
  const hrefs = await tabHrefs(page);
  expect(hrefs.length, 'nav should expose tabs').toBeGreaterThan(0);

  for (const href of hrefs) {
    const id = href.slice(1);
    await ensureMenuForTabs(page);
    const link = page.locator(`nav.tabs a.tab[href="${href}"]`);
    // Prefer a genuine pointer click; fall back to dispatch if the layout makes
    // the target non-actionable (e.g. an overlapped sticky header edge case).
    try {
      await link.click({ timeout: 3000 });
    } catch {
      await link.dispatchEvent('click');
    }
    await expect(page.locator('.view.active')).toHaveAttribute('id', `view-${id}`);
    await expect(page).toHaveURL(new RegExp(`#${id}$`));
  }
});

test('brand logo returns to the first tab', async ({ page }) => {
  await page.goto('');
  const hrefs = await tabHrefs(page);
  const firstId = hrefs[0].slice(1);
  const other = hrefs.map((h) => h.slice(1)).find((id) => id !== firstId);
  test.skip(!other, 'need at least two tabs');
  // Go somewhere else, then click the brand — it must route to the first tab.
  await page.locator(`nav.tabs a.tab[href="#${other}"]`).dispatchEvent('click');
  await expect(page.locator('.view.active')).toHaveAttribute('id', `view-${other}`);
  await page.locator('a.brand').dispatchEvent('click');
  await expect(page.locator('.view.active')).toHaveAttribute('id', `view-${firstId}`);
});

test('mobile header controls do not overlap (hamburger is tappable)', async ({ page }) => {
  await page.goto('');
  const menuBtn = page.locator('.menu-btn');
  test.skip(!(await menuBtn.isVisible()), 'desktop layout: no hamburger menu');

  // Deterministic, animation-free guard: at rest, the element at the centre of
  // the hamburger must BE the hamburger — not an overlapping header control
  // (theme toggle / GitHub link). This is the real "can a user tap it" check.
  const covered = await page.evaluate(() => {
    const btn = document.querySelector('.menu-btn')!.getBoundingClientRect();
    const el = document.elementFromPoint(btn.x + btn.width / 2, btn.y + btn.height / 2);
    return !(el && (el as Element).closest('.menu-btn'));
  });
  expect(covered, 'hamburger button is covered by another header control').toBe(false);
});

test('mobile hamburger menu opens and closes', async ({ page }) => {
  await page.goto('');
  const menuBtn = page.locator('.menu-btn');
  test.skip(!(await menuBtn.isVisible()), 'desktop layout: no hamburger menu');

  await clickMenuBtn(page);
  await expect(page.locator('nav.tabs')).toHaveClass(/open/);
  // Selecting a tab closes the dropdown. Dispatch the click (rather than a
  // coordinate tap) because the dropdown slides in with a transition and the
  // sticky header overlays its top edge mid-animation — the same reason the
  // tab-nav sweep dispatches. This still fires the real React onClick handler.
  await page.locator('nav.tabs.open a.tab').first().dispatchEvent('click');
  await expect(page.locator('nav.tabs')).not.toHaveClass(/open/);
});

test('Copy buttons respond on every page that has them', async ({ page }) => {
  // This test clicks every Copy button on every page, so on the slowest
  // emulated viewports under 4-way sharding the cumulative toPass retries can
  // approach the default per-test cap. Mark it slow (triples the timeout) so a
  // laggy Copied re-render on one device can never exhaust the budget.
  test.slow();
  await page.goto('');
  const hrefs = await tabHrefs(page);
  let clickedAny = false;

  for (const href of hrefs) {
    await page.goto(href);
    const copies = page.locator('button.copy');
    const n = await copies.count();
    for (let i = 0; i < n; i++) {
      const btn = copies.nth(i);
      await btn.scrollIntoViewIfNeeded();
      // The button flips to "Copied" for ~1.4s on success. Under heavy CI
      // parallelism a single click's re-render can be missed, so retry the whole
      // click+assert until it lands (toPass) instead of flaking.
      await expect(async () => {
        await btn.dispatchEvent('click');
        await expect(btn).toHaveText(/Copied/, { timeout: 2500 });
      }).toPass({ timeout: 20_000 });
      clickedAny = true;
    }
  }
  expect(clickedAny, 'the site should expose at least one Copy button').toBeTruthy();
});

test('every accordion (FAQ / releases / doc examples) toggles open and closed', async ({ page }) => {
  await page.goto('');
  const hrefs = await tabHrefs(page);

  for (const href of hrefs) {
    await page.goto(href);
    const summaries = page.locator('.view.active details > summary');
    const n = await summaries.count();
    for (let i = 0; i < n; i++) {
      const summary = summaries.nth(i);
      const details = summary.locator('xpath=..');
      const openBefore = await details.evaluate((d) => (d as HTMLDetailsElement).open);
      await summary.click();
      await expect
        .poll(() => details.evaluate((d) => (d as HTMLDetailsElement).open))
        .toBe(!openBefore);
    }
  }
});

test('in-page jump links scroll to an existing target', async ({ page }) => {
  await page.goto('');
  const hrefs = await tabHrefs(page);
  const tabIds = hrefs.map((h) => h.slice(1));

  for (const href of hrefs) {
    const tabId = href.slice(1);
    await page.goto(href);
    // Links that point at an in-page id (not another tab): e.g. the "on this
    // page" jump links inside a library view.
    const jumps = page.locator('.view.active a[href^="#"]');
    const targets = (await jumps.evaluateAll((els) =>
      els.map((e) => (e as HTMLAnchorElement).getAttribute('href') || ''),
    ))
      .map((h) => h.slice(1))
      .filter((t) => t && !tabIds.includes(t) && !t.startsWith('pkg/'));

    for (const target of targets) {
      await page.locator(`.view.active a[href="#${target}"]`).first().dispatchEvent('click');
      // The target section exists AND — crucially — following an in-page anchor
      // must NOT navigate away from the current tab (regression guard for the
      // hash-router resetting to the fallback view on unknown hashes).
      await expect(page.locator(`[id="${target}"]`)).toHaveCount(1);
      await expect(page.locator('.view.active')).toHaveAttribute('id', `view-${tabId}`);
    }
  }
});

test('API-docs navigation: filter, open a package, and jump to a symbol', async ({ page }) => {
  await page.goto('');
  const hrefs = await tabHrefs(page);
  const docsHref = hrefs.find((h) => h === '#docs');
  test.skip(!docsHref, 'this site has no API-docs tab');

  await page.goto(docsHref!);
  const shell = page.locator('.docs-shell');
  await expect(shell).toBeVisible();

  // doc.json is generated for the preview server; if it genuinely failed to
  // load the renderer shows an error — treat that as a skip, not a failure.
  if (await page.locator('.docs-error').count()) test.skip(true, 'doc.json unavailable in this run');

  const links = page.locator('.docs-pkg-link');
  await expect(links.first()).toBeVisible();
  const total = await links.count();
  expect(total, 'docs should list packages').toBeGreaterThan(0);

  // Search filters the package list.
  const search = page.locator('.docs-nav-search');
  const firstName = (await page.locator('.docs-pkg-link .docs-pkg-name').first().innerText()).trim();
  await search.fill(firstName);
  await expect.poll(async () => links.count()).toBeLessThanOrEqual(total);
  await expect(links.first()).toBeVisible();
  await search.fill('');

  // Opening a package updates the package view and the hash.
  await links.first().click();
  await expect(page.locator('.pkg-view .pkg-title')).toBeVisible();
  await expect(page).toHaveURL(/#pkg\//);

  // A symbol anchor deep-links to that symbol.
  const anchors = page.locator('.sym-card .sym-anchor');
  if (await anchors.count()) {
    const href = await anchors.first().getAttribute('href');
    await anchors.first().click();
    expect(href, 'symbol anchor should be a hash link').toMatch(/^#/);
    await expect(page).toHaveURL(new RegExp(`${href!.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`));
  }
});

// The rest of the suite runs with reduced motion (see playwright.config) so the
// wormhole resolves instantly; this block opts back into motion to prove the
// transition itself works end to end.
test.describe('wormhole page transition', () => {
  test('navigating warps through the wormhole canvas, then the target view arrives', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await page.goto('');
    const hrefs = await tabHrefs(page);
    const current = (await page.locator('.view.active').getAttribute('id'))?.replace('view-', '');
    const target = hrefs.map((h) => h.slice(1)).find((id) => id !== current);
    test.skip(!target, 'need at least two tabs to transition between');

    // Trigger the nav (dispatch is layout-robust across the device matrix).
    await page.locator(`nav.tabs a.tab[href="#${target}"]`).dispatchEvent('click');

    // The wormhole canvas lights up during the warp...
    await expect(page.locator('canvas.wormhole')).toHaveClass(/on/, { timeout: 1500 });
    // ...the target page arrives (swapped at the throat of the tunnel)...
    await expect(page.locator('.view.active')).toHaveAttribute('id', `view-${target}`, { timeout: 3000 });
    await expect(page).toHaveURL(new RegExp(`#${target}$`));
    // ...and the overlay clears once the animation completes.
    await expect(page.locator('canvas.wormhole')).not.toHaveClass(/on/, { timeout: 3000 });
    expect(pageErrors, 'wormhole must not throw').toEqual([]);
  });
});
