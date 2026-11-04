// Prueba del modo wake + corte: node test-wake.mjs  (PORT default 8144). Guarda los clips en out/clip-N.wav
import { spawn } from 'node:child_process'; import puppeteer from 'puppeteer-core'; import path from 'node:path'; import fs from 'node:fs';
const PORT = process.env.PORT || '8144', dir = path.resolve(import.meta.dirname); fs.mkdirSync(path.join(dir, 'out'), { recursive: true });
const srv = spawn(process.execPath, ['serve.mjs'], { cwd: dir, env: { ...process.env, PORT }, stdio: 'ignore' });
await new Promise(r => setTimeout(r, 800));
const browser = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: true });
try {
  for (const wav of process.argv.slice(2)) {
    const page = await browser.newPage(); await page.goto(`http://127.0.0.1:${PORT}/`);
    await page.waitForFunction(() => document.getElementById('status').textContent.startsWith('listo'), { timeout: 60000 });
    await page.select('#mode', 'wake');
    await page.evaluate(w => __test.feed(w), wav);
    await new Promise(r => setTimeout(r, 4000));
    const clips = await page.$$eval('.clip', cs => cs.map(c => ({ meta: c.querySelector('.meta').textContent, txt: c.querySelector('.txt').textContent.trim(), url: c.querySelector('audio').src })));
    console.log(`${wav}: ${clips.length} clip(s)`);
    for (const [i, c] of clips.entries()) {
      const b64 = await page.evaluate(async u => { const b = new Uint8Array(await (await fetch(u)).arrayBuffer()); let s = ''; b.forEach(x => s += String.fromCharCode(x)); return btoa(s); }, c.url);
      const f = path.join(dir, 'out', `${path.basename(wav, '.wav')}-clip${i + 1}.wav`); fs.writeFileSync(f, Buffer.from(b64, 'base64'));
      console.log('  ', c.meta, '\n   texto:', c.txt, '\n   ->', f);
    }
    console.log('  log:', (await page.$eval('#log', e => e.textContent)).trim().split('\n').join(' | '));
    await page.close();
  }
} finally { await browser.close(); srv.kill(); }
