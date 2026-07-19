import { chromium } from 'playwright';
const BASE = process.env.BASE || 'http://api:8080';
const errors = [];
const browser = await chromium.launch();
const page = await browser.newPage();
await page.setViewportSize({ width: 1280, height: 900 });
page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
page.on('pageerror', e => errors.push('PAGEERROR: ' + e.message));
await page.goto(BASE + '/', { waitUntil: 'networkidle' });
await page.click('[data-nav="settings"]');
await page.waitForTimeout(800);
await page.click('#toggleDomainMgr').catch(() => {});
await page.waitForTimeout(600);
const info = await page.evaluate(() => ({
  heading: document.querySelector('#section-settings h1,#section-settings h2')?.textContent?.trim() || '',
  buttons: [...document.querySelectorAll('#section-settings button')].map(b => b.textContent.trim()).filter(Boolean).slice(0, 8),
}));
await page.screenshot({ path: '/work/testdata/shots/4-settings.png' });
console.log(JSON.stringify({ info, consoleErrors: errors }, null, 2));
await browser.close();
