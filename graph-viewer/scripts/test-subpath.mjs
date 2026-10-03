// Regression: the loader requested /sql-wasm.wasm even when deployed below /cairn/.
// Serve only the deployment subpath, then load a real SQLite fixture through built DBLoader code.
import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { mkdtemp, readFile, writeFile, rm, mkdir } from 'node:fs/promises'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { build } from 'vite'
import initSqlJs from 'sql.js'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
await mkdir(join(root, '.tmp'), { recursive: true })
const temporary = await mkdtemp(join(root, '.tmp', 'subpath-'))
const originalFetch = globalThis.fetch
const requested = []
let server
try {
  await build({
    configFile: false,
    root,
    publicDir: join(root, 'public'),
    base: './',
    logLevel: 'warn',
    build: {
      outDir: temporary,
      assetsInlineLimit: 0,
      lib: { entry: join(root, 'src/services/db-loader.ts'), formats: ['es'], fileName: 'loader' },
      rollupOptions: { external: ['sql.js'] },
    },
  })
  const SQL = await initSqlJs({ wasmBinary: await readFile(join(root, 'public/sql-wasm.wasm')) })
  const fixture = new SQL.Database()
  fixture.run("CREATE TABLE nodes (id TEXT, label TEXT, name TEXT, domain TEXT); CREATE TABLE edges (id INTEGER, source_id TEXT, target_id TEXT, kind TEXT); INSERT INTO nodes VALUES ('fixture','Concept','子路径验证','demo')")
  const fixtureBytes = Buffer.from(fixture.export())
  fixture.close()
  server = createServer(async (request, response) => {
    requested.push(request.url)
    const path = new URL(request.url, 'http://localhost').pathname
    // No root-level fallback: the old absolute URL must fail this test.
    if (!path.startsWith('/cairn/')) { response.writeHead(404).end(); return }
    try {
      if (path === '/cairn/fixture.db') { response.end(fixtureBytes); return }
      const relative = path.slice('/cairn/'.length)
      const base = relative === 'sql-wasm.wasm' ? temporary : join(root, 'dist')
      const bytes = await readFile(join(base, relative || 'index.html'))
      if (path.endsWith('.wasm')) response.setHeader('Content-Type', 'application/wasm')
      response.end(bytes)
    } catch { response.writeHead(404).end() }
  })
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve) })
  const entry = new URL(`http://127.0.0.1:${server.address().port}/cairn/`)
  globalThis.fetch = (input, init) => originalFetch(new URL(input, entry), init)
  // Verify the actual application build is served with relative JS/CSS URLs.
  const htmlResponse = await fetch(entry)
  assert.equal(htmlResponse.status, 200)
  const html = await htmlResponse.text()
  for (const match of html.matchAll(/(?:src|href)="([^"]+)"/g)) {
    if (!match[1].startsWith('./assets/')) continue
    assert.equal((await fetch(new URL(match[1], entry))).status, 200, `missing built asset ${match[1]}`)
  }
  // Node imports local ESM; simulate the module's served browser URL without changing loader logic.
  const modulePath = join(temporary, 'loader.js')
  const moduleSource = await readFile(modulePath, 'utf8')
  await writeFile(modulePath, moduleSource.replaceAll('import.meta.url', JSON.stringify(new URL('loader.js', entry).href)))
  const { DBLoader } = await import(pathToFileURL(modulePath).href)
  const loader = new DBLoader()
  try {
    const graph = await loader.loadFromURL(new URL('fixture.db', entry).href)
    assert.equal(graph.meta.nodeCount, 1)
    assert.equal(graph.nodes[0].name, '子路径验证')
    assert(requested.includes('/cairn/sql-wasm.wasm'), `WASM requests: ${requested.join(', ')}`)
    assert(!requested.includes('/sql-wasm.wasm'), 'root WASM path leaked into subpath deployment')
  } finally { loader.close() }
  console.log('Subpath /cairn/: built JS/CSS + SQLite WASM + fixture loading passed')
} finally {
  globalThis.fetch = originalFetch
  if (server) await new Promise(resolve => server.close(resolve))
  await rm(temporary, { recursive: true, force: true })
}
