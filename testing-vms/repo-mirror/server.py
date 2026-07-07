#!/usr/bin/env python3
import base64
import gzip
import json
import os
import pathlib
import ssl
import http.client
import subprocess
import sys
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

GIT_ROOT = pathlib.Path('/srv/git')
RELEASE_ROOT = pathlib.Path('/srv/releases')


def asset_id(owner, repo, tag, name):
    raw = f'{owner}/{repo}/{tag}/{name}'.encode()
    return base64.urlsafe_b64encode(raw).decode().rstrip('=')


def decode_asset_id(value):
    padding = '=' * (-len(value) % 4)
    raw = base64.urlsafe_b64decode((value + padding).encode()).decode()
    parts = raw.split('/', 3)
    if len(parts) != 4:
        raise ValueError('bad asset id')
    return parts


def release_dir(owner, repo, tag):
    return RELEASE_ROOT / owner / repo / tag


class Handler(BaseHTTPRequestHandler):
    server_version = 'opendeploy-repo-mirror/1.0'

    def do_GET(self):
        self.handle_request()

    def do_POST(self):
        self.handle_request()

    def handle_request(self):
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path == '/healthz':
            self.send_bytes(200, b'ok\n', 'text/plain')
            return
        if parsed.path.startswith('/repos/'):
            if not self.local_release_api_path(parsed):
                self.proxy_unknown(parsed)
                return
            self.handle_api(parsed)
            return
        if '/releases/download/' in parsed.path:
            if not self.local_download_path(parsed):
                self.proxy_unknown(parsed)
                return
            self.handle_download(parsed)
            return
        if '.git' in parsed.path:
            if not self.local_git_path(parsed):
                self.proxy_unknown(parsed)
                return
            self.handle_git(parsed)
            return
        self.proxy_unknown(parsed)

    def local_release_api_path(self, parsed):
        parts = parsed.path.strip('/').split('/')
        if len(parts) < 4 or parts[0] != 'repos' or parts[3] != 'releases':
            return False
        return (RELEASE_ROOT / parts[1] / parts[2]).exists()

    def local_download_path(self, parsed):
        parts = parsed.path.strip('/').split('/')
        try:
            idx = parts.index('releases')
        except ValueError:
            return False
        if idx < 2 or len(parts) < idx + 4 or parts[idx + 1] != 'download':
            return False
        owner = '/'.join(parts[:idx - 1])
        repo = parts[idx - 1]
        return (RELEASE_ROOT / owner / repo).exists()

    def local_git_path(self, parsed):
        prefix = parsed.path.strip('/').split('.git', 1)[0]
        if not prefix:
            return False
        return (GIT_ROOT / f'{prefix}.git').exists()

    def proxy_unknown(self, parsed):
        mode = os.environ.get('OPD_REPO_MIRROR_PROXY_UNKNOWN', 'false').lower()
        host = self.headers.get('Host', '').split(':', 1)[0]
        if mode in ('', 'false', '0', 'no', 'none'):
            self.send_error(404, 'not found')
            return
        if host not in ('github.com', 'api.github.com'):
            self.send_error(404, 'not found')
            return
        if mode == 'nixpkgs' and not self.is_nixpkgs_proxy_path(host, parsed.path):
            self.send_error(404, 'not found')
            return
        body = self.read_request_body()
        if body is None:
            return
        headers = {}
        skip = {'authorization', 'connection', 'content-length', 'host', 'keep-alive', 'proxy-authenticate', 'proxy-authorization', 'te', 'trailers', 'transfer-encoding', 'upgrade'}
        for key, value in self.headers.items():
            if key.lower() not in skip:
                headers[key] = value
        headers['Host'] = host
        conn = http.client.HTTPSConnection(host, timeout=120)
        try:
            conn.request(self.command, self.path, body=body, headers=headers)
            resp = conn.getresponse()
            status = resp.status
            reason = resp.reason
            resp_headers = resp.getheaders()
            data = resp.read()
        except Exception as exc:
            self.send_error(502, f'proxy failed: {exc}')
            return
        finally:
            conn.close()
        self.send_response(status, reason)
        for key, value in resp_headers:
            if key.lower() not in skip:
                self.send_header(key, value)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def is_nixpkgs_proxy_path(self, host, path):
        clean = path.strip('/')
        if host == 'api.github.com':
            return clean.startswith('repos/NixOS/nixpkgs')
        return clean.startswith('NixOS/nixpkgs')

    def handle_api(self, parsed):
        parts = parsed.path.strip('/').split('/')
        if len(parts) >= 6 and parts[0] == 'repos' and parts[3] == 'releases' and parts[4] == 'assets':
            try:
                owner, repo, tag, name = decode_asset_id(parts[5])
            except Exception:
                self.send_error(404, 'asset not found')
                return
            self.serve_file(release_dir(owner, repo, tag) / name)
            return

        if len(parts) < 4 or parts[0] != 'repos' or parts[3] != 'releases':
            self.send_error(404, 'api path not found')
            return
        owner, repo = parts[1], parts[2]
        if len(parts) == 5 and parts[4] == 'latest':
            latest_file = RELEASE_ROOT / owner / repo / 'latest'
            if not latest_file.exists():
                self.send_error(404, 'latest release not found')
                return
            self.send_json(self.release_json(owner, repo, latest_file.read_text().strip()))
            return
        if len(parts) == 6 and parts[4] == 'tags':
            self.send_json(self.release_json(owner, repo, parts[5]))
            return
        if len(parts) == 4 or (len(parts) == 5 and parts[4] == ''):
            self.send_json(self.release_list(owner, repo))
            return
        self.send_error(404, 'api path not found')

    def release_list(self, owner, repo):
        root = RELEASE_ROOT / owner / repo
        if not root.exists():
            return []
        tags = [p.name for p in root.iterdir() if p.is_dir()]
        tags.sort(reverse=True)
        return [self.release_json(owner, repo, tag) for tag in tags]

    def release_json(self, owner, repo, tag):
        root = release_dir(owner, repo, tag)
        if not root.exists():
            self.send_error(404, 'release not found')
            raise RuntimeError('release not found')
        assets = []
        host = self.headers.get('Host', 'api.github.com')
        scheme = 'https' if os.environ.get('OPD_REPO_MIRROR_TLS', 'true') == 'true' else 'http'
        for path in sorted(root.iterdir()):
            if not path.is_file():
                continue
            aid = asset_id(owner, repo, tag, path.name)
            assets.append({
                'name': path.name,
                'url': f'{scheme}://{host}/repos/{owner}/{repo}/releases/assets/{aid}',
                'size': path.stat().st_size,
            })
        return {
            'tag_name': tag,
            'name': tag,
            'published_at': '2026-01-01T00:00:00Z',
            'author': {'login': 'opendeploy-repo-mirror'},
            'assets': assets,
        }

    def handle_download(self, parsed):
        parts = parsed.path.strip('/').split('/')
        try:
            idx = parts.index('releases')
        except ValueError:
            self.send_error(404, 'download not found')
            return
        if idx < 2 or len(parts) < idx + 4 or parts[idx + 1] != 'download':
            self.send_error(404, 'download not found')
            return
        owner = '/'.join(parts[:idx - 1])
        repo = parts[idx - 1]
        tag = parts[idx + 2]
        name = '/'.join(parts[idx + 3:])
        if '/' in owner or '/' in repo or '/' in tag or name == '':
            self.send_error(404, 'download not found')
            return
        self.serve_file(release_dir(owner, repo, tag) / name)

    def handle_git(self, parsed):
        body = self.read_request_body()
        if body is None:
            return
        if self.headers.get('Content-Encoding', '').lower() == 'gzip':
            body = gzip.decompress(body)
        git_protocol = self.headers.get('Git-Protocol', '')
        remote_addr = self.client_address[0] if self.client_address else ''
        env = os.environ.copy()
        env.update({
            'GIT_PROJECT_ROOT': str(GIT_ROOT),
            'GIT_HTTP_EXPORT_ALL': '1',
            'REQUEST_METHOD': self.command,
            'PATH_INFO': parsed.path,
            'QUERY_STRING': parsed.query,
            'REMOTE_ADDR': remote_addr,
            'REMOTE_USER': '',
            'CONTENT_TYPE': self.headers.get('Content-Type', ''),
            'CONTENT_LENGTH': str(len(body)),
            'HTTP_GIT_PROTOCOL': git_protocol,
        })
        proc = subprocess.run(['git', 'http-backend'], input=body, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env)
        if proc.returncode != 0:
            sys.stderr.buffer.write(proc.stderr)
            self.send_error(500, 'git backend failed')
            return
        header_blob, _, response_body = proc.stdout.partition(b'\r\n\r\n')
        if not response_body:
            header_blob, _, response_body = proc.stdout.partition(b'\n\n')
        status = 200
        headers = []
        for raw in header_blob.replace(b'\r\n', b'\n').split(b'\n'):
            if not raw:
                continue
            key, _, value = raw.decode(errors='replace').partition(':')
            if key.lower() == 'status':
                status = int(value.strip().split()[0])
            elif key and value:
                headers.append((key, value.strip()))
        self.send_response(status)
        for key, value in headers:
            self.send_header(key, value)
        self.send_header('Content-Length', str(len(response_body)))
        self.end_headers()
        self.wfile.write(response_body)

    def read_request_body(self):
        if self.command == 'POST':
            if self.headers.get('Transfer-Encoding', '').lower() == 'chunked':
                body = bytearray()
                while True:
                    line = self.rfile.readline().strip()
                    if not line:
                        self.send_error(400, 'bad chunked body')
                        return None
                    size = int(line.split(b';', 1)[0], 16)
                    if size == 0:
                        self.rfile.readline()
                        return bytes(body)
                    body.extend(self.rfile.read(size))
                    self.rfile.read(2)

            length = int(self.headers.get('Content-Length', '0') or '0')
            return self.rfile.read(length)
        return b''

    def serve_file(self, path):
        path = path.resolve()
        if not str(path).startswith(str(RELEASE_ROOT)) or not path.is_file():
            self.send_error(404, 'file not found')
            return
        self.send_bytes(200, path.read_bytes(), 'application/octet-stream')

    def send_json(self, value):
        self.send_bytes(200, json.dumps(value).encode() + b'\n', 'application/json')

    def send_bytes(self, status, data, content_type):
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)


def main():
    port = int(os.environ.get('OPD_REPO_MIRROR_PORT', '443'))
    httpd = ThreadingHTTPServer(('0.0.0.0', port), Handler)
    cert = os.environ.get('OPD_REPO_MIRROR_CERT')
    key = os.environ.get('OPD_REPO_MIRROR_KEY')
    if cert and key:
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        context.load_cert_chain(cert, key)
        httpd.socket = context.wrap_socket(httpd.socket, server_side=True)
    httpd.serve_forever()


if __name__ == '__main__':
    main()
