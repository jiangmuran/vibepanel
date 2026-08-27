# A stand-in for github.muran.tech: 401 with a verification block until a file
# appears on disk, then serve /<full-url> out of a directory.
import http.server, os, sys, urllib.parse

ROOT, FLAG = sys.argv[2], sys.argv[3]
NOTICE = (
    "┌─ GitHub mirror · Access Verification ─┐\n\n"
    "  Code:   DEADBEEF\n\n"
    "    https://mirror.invalid/verify?code=DEADBEEF\n\n"
    "└───┘\n"
)

class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        if not os.path.exists(FLAG):
            b = NOTICE.encode()
            self.send_response(401)
            self.send_header('Content-Type', 'text/plain; charset=utf-8')
            self.send_header('Content-Length', str(len(b)))
            self.end_headers()
            self.wfile.write(b)
            return
        # /<scheme>://<host>/<path...>. A GitHub mirror proxies GitHub and
        # nothing else, and this one refuses anything else too -- otherwise it
        # happily serves a rerouted internal URL and the check cannot tell
        # "only GitHub is rerouted" from "everything is".
        target = urllib.parse.unquote(self.path).lstrip('/')
        if not target.startswith(('https://github.com/',
                                  'https://raw.githubusercontent.com/',
                                  'https://api.github.com/',
                                  'https://objects.githubusercontent.com/')):
            self.send_error(403, 'this mirror only proxies github'); return
        name = os.path.basename(target)
        if name == 'install.sh':
            # install.sh's readiness probe. It only asks whether the mirror
            # answers at all, so the body does not matter -- but it has to be a
            # 200, or an authorised mirror reads as an unauthorised one.
            b = b'#!/bin/sh\n'
        else:
            p = os.path.join(ROOT, name)
            if not os.path.isfile(p):
                self.send_error(404); return
            b = open(p, 'rb').read()
        self.send_response(200)
        self.send_header('Content-Type', 'application/octet-stream')
        self.send_header('Content-Length', str(len(b)))
        self.end_headers()
        self.wfile.write(b)

http.server.HTTPServer(('127.0.0.1', int(sys.argv[1])), H).serve_forever()
