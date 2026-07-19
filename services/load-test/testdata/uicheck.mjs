import { chromium } from 'playwright';

const BASE = process.env.BASE || 'http://api:8080';
const errors = [];
const browser = await chromium.launch();
const page = await browser.newPage();
page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
page.on('pageerror', e => errors.push('PAGEERROR: ' + e.message));

await page.goto(BASE + '/', { waitUntil: 'networkidle' });

const title = await page.title();
const shell = await page.evaluate(() => ({
  app: !!document.querySelector('.app'),
  sidebar: !!document.querySelector('.sidebar'),
  topbar: !!document.querySelector('.topbar'),
  navItems: [...document.querySelectorAll('.sidebar .nav-btn')].map(b => b.textContent.trim().replace(/\s+/g, ' ')),
  views: document.querySelectorAll('.view').length,
}));

let navSwitch = 'n/a';
try {
  const before = await page.evaluate(() => [...document.querySelectorAll('.view')].findIndex(v => !v.hidden));
  const btns = await page.$$('.sidebar .nav-btn');
  for (const b of btns) {
    const t = (await b.textContent()).trim();
    if (t.includes('부하 테스트')) { await b.click(); break; }
  }
  await page.waitForTimeout(300);
  const after = await page.evaluate(() => [...document.querySelectorAll('.view')].findIndex(v => !v.hidden));
  navSwitch = `visibleView ${before} -> ${after}`;
} catch (e) { navSwitch = 'ERR ' + e.message; }

const formProbe = await page.evaluate(() => ({
  hasUrlInput: !!document.querySelector('input[type="text"], input[type="url"], input:not([type])'),
  hasRange: !!document.querySelector('input[type="range"]'),
  hasStartBtn: [...document.querySelectorAll('button')].some(b => /시작|시작하기|테스트 시작/.test(b.textContent)),
}));

console.log(JSON.stringify({ title, shell, navSwitch, formProbe, consoleErrors: errors }, null, 2));
await browser.close();
