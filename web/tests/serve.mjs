import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
const paths = { '/app': ['index.html', 'text/html'], '/assets/app.js': ['app.js', 'text/javascript'], '/assets/style.css': ['style.css', 'text/css'] };
createServer(async (request, response) => {
  const file = paths[new URL(request.url, 'http://127.0.0.1').pathname];
  if (!file) { response.writeHead(404).end(); return; }
  try { const data = await readFile(new URL('../../bus/hostedweb/' + file[0], import.meta.url)); response.writeHead(200, { 'content-type': file[1] }).end(data); }
  catch { response.writeHead(500).end(); }
}).listen(4794, '127.0.0.1');
