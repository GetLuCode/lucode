// Autoprueba de frase inicial y verificacion libre: node test-modos.mjs (PORT default 8140)
import { spawn } from 'node:child_process'; import puppeteer from 'puppeteer-core'; import path from 'node:path';
const PORT = process.env.PORT || '8140', dir = path.resolve(import.meta.dirname);
const srv = spawn(process.execPath, ['serve.mjs'], { cwd: dir, env: { ...process.env, PORT }, stdio: 'ignore' });
await new Promise(r => setTimeout(r, 800));
const browser = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: true });
const cases = [ // [prefijo, verificar, wav]
  ['', false, 'pedrito_continuar.wav'], ['', true, 'pedrito_continuar.wav'], ['', true, 'continuar.wav'],
  ['oye asistente', false, 'continuar.wav'], ['oye asistente', false, 'oye_asistente_continuar.wav'], ['oye asistente', false, 'pedrito_continuar.wav'],
  ['oye asistente', true, 'oye_asistente_continuar.wav'], ['oye asistente', true, 'pedrito_oye_asistente.wav'],
];
try {
  for (const [pre, ver, wav] of cases) {
    const page = await browser.newPage(); await page.goto(`http://127.0.0.1:${PORT}/`);
    await page.waitForFunction(() => document.getElementById('status').textContent.startsWith('listo'), { timeout: 60000 });
    await page.$eval('#prefix', (e, v) => { e.value = v; e.dispatchEvent(new Event('change')); }, pre);
    await page.evaluate(v => { const c = document.getElementById('verify'); if (c.checked !== v) c.click(); }, ver);
    await page.evaluate(w => __test.feed(w), wav);
    await new Promise(r => setTimeout(r, ver ? 4500 : 1500));
    const log = (await page.$eval('#log', e => e.textContent)).trim().split('\n').map(l => l.replace(/^\S+ \S+\s+/, '')).join(' | ');
    const free = ver ? await page.$eval('#freefinal', e => e.textContent) : '';
    console.log(`prefijo="${pre}" verificar=${ver} ${wav.padEnd(24)} -> ${log || '(nada)'}${ver ? '   [libre: ' + free + ']' : ''}`);
    await page.close();
  }
} finally { await browser.close(); srv.kill(); }

