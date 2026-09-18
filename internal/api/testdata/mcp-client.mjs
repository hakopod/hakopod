
import assert from 'node:assert/strict'
import { pathToFileURL } from 'node:url'
import { join } from 'node:path'
const sdk = path => import(pathToFileURL(join(process.env.MCP_TEST_SDK, 'dist/esm', path)).href)
const { Client } = await sdk('client/index.js')
const { StreamableHTTPClientTransport } = await sdk('client/streamableHttp.js')
const client = new Client({ name: 'hakopod-interoperability', version: '1' })
const transport = new StreamableHTTPClientTransport(new URL(process.env.MCP_TEST_URL), {
  requestInit: { headers: { Authorization: 'Bearer ' + process.env.MCP_TEST_KEY } },
})
await client.connect(transport)
try {
  await client.ping()
  const { tools } = await client.listTools()
  for (const name of ['applications', 'application', 'services', 'service', 'service_runtime', 'domains', 'provenance'])
    assert.ok(tools.some(tool => tool.name === name), name)
  assert.ok(!tools.some(tool => tool.name === 'deploy'))
  const result = await client.callTool({ name: 'applications', arguments: {} })
  assert.ok(!result.isError)
  assert.equal(result.structuredContent.content_is_untrusted, true)
  const denied = await client.callTool({ name: 'service', arguments: { application_id: 'missing', service: 'api' } })
  assert.equal(denied.isError, true)
  assert.ok(transport.sessionId)
  await transport.terminateSession()
  console.log('Official MCP SDK: initialize, ping, tools, scoped reads, tool errors and session termination passed.')
} finally {
  await client.close()
}
