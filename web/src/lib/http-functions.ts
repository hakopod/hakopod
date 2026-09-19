import type { Service } from './types'

export const serverlessDefaults = {
  min_replicas: 0,
  idle_seconds: 300,
  startup_timeout_seconds: 60,
  request_timeout_seconds: 60,
  max_concurrency: 16,
}

export function httpFunction(language: 'javascript' | 'python'): Service {
  const javascript = `import { createServer } from 'node:http';

// Read secrets from process.env. Never place credentials in this file.
createServer(async (request, response) => {
  response.writeHead(200, { 'Content-Type': 'application/json' });
  response.end(JSON.stringify({ message: 'Hello from Hakopod', path: request.url }));
}).listen(8080, '0.0.0.0');
`
  const python = `from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os

# Read secrets from os.environ. Never place credentials in this file.
class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"message": "Hello from Hakopod", "path": self.path}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

ThreadingHTTPServer(('0.0.0.0', 8080), Handler).serve_forever()
`
  const path = language === 'javascript' ? '/app/function.mjs' : '/app/function.py'
  return {
    image: language === 'javascript' ? 'node:24-alpine' : 'python:3.13.15-alpine',
    public: true,
    port: 8080,
    size: 'small',
    replicas: 1,
    command: language === 'javascript' ? ['node', path] : ['python', '-u', path],
    args: [],
    serverless: { ...serverlessDefaults },
    files: {
      function: {
        mount_path: path,
        content: language === 'javascript' ? javascript : python,
        mode: 292,
      },
    },
  }
}
