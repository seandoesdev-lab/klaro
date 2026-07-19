import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://api:8080';
const errors = [];
const browser = await chromium.launch();
const page = await browser.newPage();
page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
page.on('pageerror', e => errors.push('PAGEERROR: ' + e.message));

await page.goto(BASE + '/', { waitUntil: 'networkidle' });

const navs = ['home', 'loadtest', 'scan', 'apm', 'report', 'settings'];
const results = {};
for (const nav of navs) {
  try {
    await page.click(`[data-nav="${nav}"]`);
    await page.waitForTimeout(500);
    const info = await page.evaluate(() => {
      const secs = [...document.querySelectorAll('section[id^="section-"]')];
      const vis = secs.find(s => s.offsetParent !== null);
      if (!vis) return { visibleSection: null };
      const h = vis.querySelector('h1,h2,.page-head');
      return {
        visibleSection: vis.id,
        heading: h ? h.textContent.trim().replace(/\s+/g, ' ').slice(0, 36) : '',
        buttons: vis.querySelectorAll('button').length,
      };
    });
    results[nav] = info;
  } catch (e) { results[nav] = { error: e.message }; }
}

await page.click('[data-nav="apm"]').catch(() => {});
await page.waitForTimeout(300);
const apmProbe = await page.evaluate(() => {
  const btns = [...document.querySelectorAll('button')].map(b => b.textContent.trim());
  return { hasApmBtns: btns.some(t => /샘플|데모|트레이스|에이전트/.test(t)) };
});

console.log(JSON.stringify({ views: results, apmProbe, consoleErrors: errors }, null, 2));
await browser.close();
