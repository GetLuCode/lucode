// Autoprueba del playground: node test-playground.mjs  (servidor propio en PORT, default 8139)
// Mic falso de Chrome reproduciendo un wav; guarda playground.png (solo la pagina, headless).
import { spawn } from 'node:child_process'; import puppeteer from 'puppeteer-core'; import path from 'node:path';
const PORT = process.env.PORT || '8139', dir = path.resolve(import.meta.dirname);
const srv = spawn(process.execPath, ['serve.mjs'], { cwd: dir, env: { ...process.env, PORT }, stdio: 'ignore' });
await new Promise(r => setTimeout(r, 800));
const wav = path.join(dir, process.argv[2] || 'hola_como_estas_sabina.wav');
const browser = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: true,
  args: ['--use-fake-ui-for-media-stream', '--use-fake-device-for-media-stream', `--use-file-for-fake-audio-capture=${wav}`] });
try {
  const page = await browser.newPage(); await page.setViewport({ width: 1000, height: 600 });
  await page.goto(`http://127.0.0.1:${PORT}/`);
  await page.waitForFunction(() => document.getElementById('status').textContent.startsWith('listo'), { timeout: 60000 });
  await page.click('#mic');
  await page.waitForFunction(() => document.querySelector('.chip.hit'), { timeout: 15000 });
  await page.screenshot({ path: path.join(dir, 'playground.png') });
  console.log('OK chip iluminado:', await page.$eval('.chip.hit', e => e.dataset.phrase), '| final:', await page.$eval('#final', e => e.textContent));
} catch (e) { console.log('FALLO', e.message); process.exitCode = 1; }
finally { await browser.close(); srv.kill(); }
