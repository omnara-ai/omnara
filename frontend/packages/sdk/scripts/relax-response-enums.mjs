import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const sdkGenPath = join(dirname(fileURLToPath(import.meta.url)), '../src/generated/sdk.gen.ts')
const source = readFileSync(sdkGenPath, 'utf8')

const validatorPattern = /async \(data\) => await (z\w+)\.parseAsync\(data\)/g
if (!validatorPattern.test(source)) {
  throw new Error('no responseValidator expressions found in sdk.gen.ts; generator output may have changed')
}

const rewritten = source
  .replace(validatorPattern, 'async (data) => await validateResponse($1, data)')
  .replace(
    "import { client } from './client.gen';",
    "import { client } from './client.gen';\nimport { validateResponse } from '../validate-response';",
  )

writeFileSync(sdkGenPath, rewritten)
