import http from 'node:http'; import fs from 'node:fs'; import path from 'node:path'; import puppeteer from 'puppeteer-core';
const PORT = +process.env.PORT || 8137, dir = path.resolve(import.meta.dirname);
const types = { '.html': 'text/html', '.js': 'text/javascript', '.wav': 'audio/wav', '.gz': 'application/gzip' };
const srv = http.createServer((q, s) => {
  const f = path.join(dir, decodeURIComponent(q.url.split('?')[0]).replace(/^\/$/, '/index.html'));
  if (!f.startsWith(dir) || !fs.existsSync(f)) { s.writeHead(404).end(); return; }
  s.writeHead(200, { 'content-type': types[path.extname(f)] || 'application/octet-stream' }); fs.createReadStream(f).pipe(s);
}).listen(PORT, '127.0.0.1');
const browser = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: true, args: ['--autoplay-policy=no-user-gesture-required'] });
const page = await browser.newPage();
page.on('console', m => console.log('[page]', m.text())); page.on('pageerror', e => console.log('[pageerror]', e.message));
await page.goto(`http://127.0.0.1:${PORT}/`);
const G = process.env.GRAMMAR ? JSON.parse(process.env.GRAMMAR) : null;
for (const w of process.argv.slice(2)) console.log(JSON.stringify(await page.evaluate((u, g) => runTest(u, g), w, G)));
await browser.close(); srv.close();
