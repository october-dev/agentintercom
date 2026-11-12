import { build } from 'esbuild';
import { copyFile, mkdir } from 'node:fs/promises';
const output = new URL('../bus/hostedweb/', import.meta.url);
await mkdir(output, { recursive: true });
await build({ entryPoints: ['src/app.js'], bundle: true, minify: true, format: 'esm', target: ['es2022'], outfile: new URL('app.js', output).pathname, legalComments: 'linked' });
await Promise.all(['index.html', 'style.css'].map(name => copyFile(new URL(`src/${name}`, import.meta.url), new URL(name, output))));
