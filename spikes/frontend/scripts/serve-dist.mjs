import { createReadStream, existsSync, statSync } from 'node:fs'
import { createServer } from 'node:http'
import { extname, join, normalize } from 'node:path'

const root = normalize(join(process.cwd(), 'dist'))
const contentTypes = {
  '.css': 'text/css; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
}

createServer((request, response) => {
  const rawPath = decodeURIComponent(new URL(request.url ?? '/', 'http://localhost').pathname)
  const relativePath = rawPath.replace(/^\/+/, '')
  let filePath = normalize(join(root, relativePath))
  if (!filePath.startsWith(root)) {
    response.writeHead(403).end()
    return
  }
  if (existsSync(filePath) && statSync(filePath).isDirectory()) filePath = join(filePath, 'index.html')
  if (!existsSync(filePath)) {
    response.writeHead(404).end()
    return
  }
  response.writeHead(200, { 'Content-Type': contentTypes[extname(filePath)] ?? 'application/octet-stream' })
  createReadStream(filePath).pipe(response)
}).listen(4173, '127.0.0.1')
