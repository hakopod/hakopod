import { spawn } from 'node:child_process'
import { mkdir, mkdtemp, rm } from 'node:fs/promises'
import { resolve } from 'node:path'
import { build } from 'vite'

// Reuse the existing TSX toolchain; no DOM simulator or test runtime ships to users.
await mkdir('work', { recursive: true })
const output = await mkdtemp(resolve('work', 'ui-tests-'))
try {
  await build({
    configFile: false,
    logLevel: 'error',
    ssr: { noExternal: true },
    build: {
      ssr: 'src/components/shared.test.tsx',
      outDir: output,
      emptyOutDir: false,
      minify: false,
      rollupOptions: { output: { entryFileNames: 'regression.mjs' } },
    },
  })
  process.exitCode = await new Promise((resolveExit, reject) => {
    const child = spawn(process.execPath, ['--test', resolve(output, 'regression.mjs')], {
      stdio: 'inherit',
    })
    child.once('error', reject)
    child.once('exit', (code) => resolveExit(code ?? 1))
  })
} finally {
  await rm(output, { recursive: true, force: true })
}
