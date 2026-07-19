import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://api:8080';
const out = { steps: [], consoleErrors: [] };
const log = (k, v) => out.steps.push({ [k]: v });

const browser = await chromium.launch();
const page = await browser.newPage();
page.on('console', m => { if (m.type() === 'error') out.consoleErrors.push(m.text()); });
page.on('pageerror', e => out.consoleErrors.push('PAGEERROR: ' + e.message));

await page.goto(BASE + '/', { waitUntil: 'networkidle' });

const visibleView = () => page.evaluate(() =>
  [...document.querySelectorAll('.view')].findIndex(v => !v.hidden));

await page.click('[data-nav="home"]'); await page.waitForTimeout(200);
const homeView = await visibleView();
await page.click('[data-nav="loadtest"]'); await page.waitForTimeout(200);
const loadView = await visibleView();
log('viewSwitch', { home: homeView, loadtest: loadView, switches: homeView !== loadView });

await page.evaluate(() => {
  document.querySelectorAll('input[type=range]').forEach(r => {
    r.value = r.min || '1';
    r.dispatchEvent(new Event('input', { bubbles: true }));
    r.dispatchEvent(new Event('change', { bubbles: true }));
  });
});

await page.fill('#targetUrl', 'http://target:80');
await page.evaluate(() => {
  const el = document.querySelector('#targetUrl');
  el.dispatchEvent(new Event('input', { bubbles: true }));
  el.dispatchEvent(new Event('change', { bubbles: true }));
  el.dispatchEvent(new Event('blur', { bubbles: true }));
});
await page.waitForTimeout(1500);
const startState = await page.evaluate(() => ({
  disabled: document.querySelector('#btnStart')?.disabled,
  hint: document.querySelector('#startHint')?.textContent?.trim(),
}));
log('startButton', startState);

await page.click('#btnStart');
log('clickedStart', true);

const kpiSnap = () => page.evaluate(() =>
  [...document.querySelectorAll('.kpi-value')].map(e => e.textContent.trim()));
const samples = [];
let resultShown = false;
for (let i = 0; i < 20; i++) {
  await page.waitForTimeout(1500);
  const kv = await kpiSnap();
  const res = await page.evaluate(() => {
    const r = document.querySelector('#resultTarget');
    return r && r.offsetParent !== null ? r.textContent.trim() : null;
  });
  samples.push({ t: (i + 1) * 1.5 + 's', kpi: kv, result: res });
  if (res) { resultShown = true; break; }
}
log('kpiSamples', samples.filter((_, i) => i % 2 === 0 || i === samples.length - 1));
log('resultShown', resultShown);

console.log(JSON.stringify(out, null, 2));
await browser.close();
