import { chromium } from 'playwright';
import { mkdirSync } from 'fs';

const BASE = process.env.BASE || 'http://api:8080';
const SHOTS = '/work/testdata/shots';
mkdirSync(SHOTS, { recursive: true });
const errors = [];
const out = {};
const browser = await chromium.launch();
const page = await browser.newPage();
await page.setViewportSize({ width: 1280, height: 900 });
page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
page.on('pageerror', e => errors.push('PAGEERROR: ' + e.message));

const nav = async (n) => { await page.click(`[data-nav="${n}"]`); await page.waitForTimeout(500); };
const setVal = (sel, val) => page.evaluate(([s, v]) => {
  const el = document.querySelector(s); if (!el) return false;
  el.value = v; el.dispatchEvent(new Event('input', { bubbles: true }));
  el.dispatchEvent(new Event('change', { bubbles: true })); return true;
}, [sel, val]);
const shot = (n) => page.screenshot({ path: `${SHOTS}/${n}.png` });

await page.goto(BASE + '/', { waitUntil: 'networkidle' });

await nav('scan');
await setVal('#scanType', 'dast');
await setVal('#scanUrl', 'http://target:80');
await page.click('#btnRunScan').catch(() => {});
await page.waitForTimeout(4000);
out.scan = await page.evaluate(() => ({
  scanRows: document.querySelector('#scanRows')?.querySelectorAll('tr,.finding,.row,li')?.length ?? 0,
  findingsText: (document.querySelector('#section-scan')?.textContent || '').match(/Missing|Critical|High|Medium|Low|취약|헤더/g)?.slice(0, 6) || [],
  chips: document.querySelectorAll('#section-scan .chip').length,
}));
await shot('1-scan');

await nav('apm');
await page.click('#btnSeedApm').catch(() => {});
await page.waitForTimeout(2500);
await page.evaluate(() => {
  const t = document.querySelector('#apmTraces tr, #apmTraces .row, #apmTraces li, #apmTraces button');
  if (t) t.click();
});
await page.waitForTimeout(800);
out.apm = await page.evaluate(() => ({
  traceRows: document.querySelectorAll('#apmTraces tr, #apmTraces .row, #apmTraces li').length,
  waterfallBars: document.querySelectorAll('#apmWf .wf-bar, #apmWf .wf-row').length,
  logRows: document.querySelectorAll('#apmLogs .log-row, #apmLogs tr, #apmLogs li').length,
}));
await shot('2-apm');

await nav('report');
await page.click('#btnNewReport').catch(() => {});
await page.waitForTimeout(700);
await page.evaluate(() => {
  const modal = [...document.querySelectorAll('.modal, [role=dialog]')].find(m => m.offsetParent !== null);
  if (!modal) return;
  const sel = modal.querySelector('select');
  if (sel && sel.options.length > 1) { sel.selectedIndex = 1; sel.dispatchEvent(new Event('change', { bubbles: true })); }
  const btn = [...modal.querySelectorAll('button')].find(b => /생성|확인|만들/.test(b.textContent));
  if (btn) btn.click();
});
await page.waitForTimeout(2500);
out.report = await page.evaluate(() => {
  const t = document.querySelector('#reportDetail')?.textContent || document.querySelector('#section-report')?.textContent || '';
  return {
    hasGauge: !!document.querySelector('#section-report svg'),
    hasSummary: /요약|안정적|VU|병목|RPS/.test(t),
    reportRows: document.querySelectorAll('#reportRows tr, #reportRows .row, #reportRows li').length,
    snippet: t.replace(/\s+/g, ' ').slice(0, 120),
  };
});
await shot('3-report');

await nav('settings');
await page.waitForTimeout(800);
out.settings = await page.evaluate(() => ({
  domainRows: document.querySelectorAll('#section-settings table tr, #section-settings .domain-row, #section-settings li').length,
  text: (document.querySelector('#section-settings')?.textContent || '').replace(/\s+/g, ' ').slice(0, 100),
}));
await shot('4-settings');

out.consoleErrors = errors;
console.log(JSON.stringify(out, null, 2));
await browser.close();
