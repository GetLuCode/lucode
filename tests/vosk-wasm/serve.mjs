// Servidor estatico del playground: node serve.mjs  (PORT por defecto 8138, solo 127.0.0.1)
import http from 'node:http'; import fs from 'node:fs'; import path from 'node:path';
const PORT = +process.env.PORT || 8138, dir = path.resolve(import.meta.dirname);
const types = { '.html': 'text/html; charset=utf-8', '.js': 'text/javascript', '.wav': 'audio/wav', '.gz': 'application/gzip' };
http.createServer((q, s) => {
  const f = path.join(dir, decodeURIComponent(q.url.split('?')[0]).replace(/^\/$/, '/playground.html'));
  if (!f.startsWith(dir) || !fs.existsSync(f) || !fs.statSync(f).isFile()) { s.writeHead(404).end(); return; }
  s.writeHead(200, { 'content-type': types[path.extname(f)] || 'application/octet-stream' }); fs.createReadStream(f).pipe(s);
}).listen(PORT, '127.0.0.1', () => console.log(`playground http://127.0.0.1:${PORT}/`));
