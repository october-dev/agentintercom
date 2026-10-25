#!/usr/bin/env python3
"""Check coexistence routing with local stub backends and an installed Caddy."""
import http.server
import json
import os
import pathlib
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

root = pathlib.Path(__file__).resolve().parents[1]
source = (root / 'deploy/hosted/Caddyfile.coexistence').read_text()
servers = []
process = None


def backend(label):
    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            size = int(self.headers.get('Content-Length', '0'))
            if size:
                self.rfile.read(size)
            body = json.dumps({'backend': label, 'path': self.path, 'authorization': bool(self.headers.get('Authorization')),
                               'cookie': bool(self.headers.get('Cookie')), 'bytes': size}).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        do_POST = do_GET
        def log_message(self, *args):
            pass
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    server.daemon_threads = True
    servers.append(server)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server.server_port


def request(path, token='', method='GET', body=None):
    headers = {'Cookie': 'synthetic=fixture'}
    if token:
        headers['Authorization'] = token
    req = urllib.request.Request(origin+path, data=body, headers=headers, method=method)
    with urllib.request.urlopen(req, timeout=5) as response:
        return json.load(response)


with tempfile.TemporaryDirectory(prefix='october-bus-proxy-') as directory:
    try:
        standalone, legacy, marketing = backend('standalone'), backend('legacy'), backend('marketing')
        with socket.socket() as reservation:
            reservation.bind(('127.0.0.1', 0))
            port = reservation.getsockname()[1]
        origin = f'http://127.0.0.1:{port}'
        config = '{\n admin off\n auto_https off\n}\n'+source.replace('bus.october.dev {', origin+' {')
        config = config.replace('127.0.0.1:4780', f'127.0.0.1:{standalone}').replace('127.0.0.1:8787', f'127.0.0.1:{legacy}')
        config = config.replace('https://october-products.vercel.app', f'http://127.0.0.1:{marketing}')
        config = '\n'.join(line for line in config.splitlines() if 'tls_server_name' not in line)
        path = pathlib.Path(directory)/'Caddyfile'
        path.write_text(config)
        env = dict(os.environ, XDG_DATA_HOME=directory, XDG_CONFIG_HOME=directory)
        subprocess.run(['caddy', 'validate', '--adapter', 'caddyfile', '--config', str(path)], env=env, capture_output=True, check=True)
        with (pathlib.Path(directory)/'caddy.log').open('w+') as log:
            process = subprocess.Popen(['caddy', 'run', '--adapter', 'caddyfile', '--config', str(path)], env=env, stdout=log, stderr=log)
            for _ in range(50):
                if process.poll() is not None:
                    log.seek(0)
                    raise RuntimeError(log.read())
                try:
                    request('/app')
                    break
                except (OSError, urllib.error.URLError):
                    time.sleep(.1)
            else:
                raise RuntimeError('Test proxy did not become ready')
            cases = [
                ('/mcp', 'Bearer obg_fixture', 'legacy'), ('/mcp', 'bearer obg_fixture', 'legacy'),
                ('/mcp', 'Bearer obk_fixture', 'standalone'), ('/mcp', 'Bearer oba_fixture', 'standalone'),
                ('/mcp', '', 'standalone'), ('/bus/v1/agents', 'Bearer scope_fixture', 'legacy'),
                ('/api/workspaces', 'Bearer account_fixture', 'standalone'),
                ('/.well-known/oauth-protected-resource/mcp', '', 'standalone'),
                ('/oauth/token', '', 'standalone'), ('/health/ready', '', 'standalone'),
                ('/app', '', 'standalone'), ('/app/', '', 'standalone'),
                ('/assets/app.js', '', 'standalone'), ('/assets/style.css', '', 'standalone'),
                ('/assets/app.js.LEGAL.txt', '', 'standalone'),
                ('/assets/logo.svg', 'Bearer account_fixture', 'marketing'),
                ('/', 'Bearer account_fixture', 'marketing'), ('/robots.txt', '', 'marketing'),
            ]
            for path, token, expected in cases:
                result = request(path, token)
                assert result['backend'] == expected, (path, result)
                assert result['authorization'] == (bool(token) and expected != 'marketing'), (path, result)
                assert result['cookie'] == (expected != 'marketing'), (path, result)
                if path == '/':
                    assert result['path'] == '/bus'
            assert request('/assets/logo.svg', method='POST', body=b'fixture')['backend'] == 'standalone'
            assert request('/mcp', 'Bearer obg_fixture', 'POST', b'{}')['backend'] == 'legacy'
            upload = request('/v1/attachments', 'Bearer execution_fixture', 'POST', b'x'*(2*1024*1024))
            assert upload['backend'] == 'standalone' and upload['bytes'] == 2*1024*1024
            print('Hosted proxy: legacy credentials/routes, new MCP/OAuth, workspace assets, private headers and file upload passed')
    finally:
        if process is not None:
            process.terminate()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
        for server in servers:
            server.shutdown()
            server.server_close()
