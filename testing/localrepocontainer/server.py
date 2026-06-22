#!/usr/bin/env python3
import base64
import json
import os
import pathlib
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
    server_version = 'opendeploy-local-repo/1.0'

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
            self.handle_api(parsed)
            return
        if '/releases/download/' in parsed.path:
            self.handle_download(parsed)
            return
        if '.git' in parsed.path:
            self.handle_git(parsed)
            return
        self.send_error(404, 'not found')

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
        host = self.headers.get('Host', 'opendeploy-local-repo:8080')
        for path in sorted(root.iterdir()):
            if not path.is_file():
                continue
            aid = asset_id(owner, repo, tag, path.name)
            assets.append({
                'name': path.name,
                'url': f'http://{host}/repos/{owner}/{repo}/releases/assets/{aid}',
                'size': path.stat().st_size,
            })
        return {
            'tag_name': tag,
            'name': tag,
            'published_at': '2026-01-01T00:00:00Z',
            'author': {'login': 'opendeploy-local-repo'},
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
        body = b''
        if self.command == 'POST':
            length = int(self.headers.get('Content-Length', '0') or '0')
            body = self.rfile.read(length)
        env = os.environ.copy()
        env.update({
            'GIT_PROJECT_ROOT': str(GIT_ROOT),
            'GIT_HTTP_EXPORT_ALL': '1',
            'REQUEST_METHOD': self.command,
            'PATH_INFO': parsed.path,
            'QUERY_STRING': parsed.query,
            'REMOTE_USER': '',
            'CONTENT_TYPE': self.headers.get('Content-Type', ''),
            'CONTENT_LENGTH': str(len(body)),
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


if __name__ == '__main__':
    ThreadingHTTPServer(('0.0.0.0', 8080), Handler).serve_forever()
