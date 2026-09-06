import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const sdkGenPath = join(dirname(fileURLToPath(import.meta.url)), '../src/generated/sdk.gen.ts')
const importLine = "import { relaxedResponseValidator } from '../validate-response';"
const source = readFileSync(sdkGenPath, 'utf8')

const validatorPattern = /async \(data\) => await (z\w+)\.parseAsync\(data\)/g
const strictValidators = [...source.matchAll(validatorPattern)]
const relaxedValidators = source.match(/relaxedResponseValidator\(z\w+\)/g) ?? []
const alreadyRelaxed =
  strictValidators.length === 0 && relaxedValidators.length > 0 && source.includes(importLine)

if (!alreadyRelaxed) {
  if (strictValidators.length === 0) {
    throw new Error(
      'no responseValidator expressions found in sdk.gen.ts; generator output may have changed',
    )
  }
  let rewritten = source.replace(validatorPattern, 'relaxedResponseValidator($1)')
  if (!rewritten.includes(importLine)) {
    const imports = [...rewritten.matchAll(/^import .*;$/gm)]
    const lastImport = imports.at(-1)
    if (lastImport === undefined) {
      throw new Error(
        'no import statements found in sdk.gen.ts to anchor the relaxedResponseValidator import',
      )
    }
    const insertAt = lastImport.index + lastImport[0].length
    rewritten = `${rewritten.slice(0, insertAt)}\n${importLine}${rewritten.slice(insertAt)}`
  }
  if (validatorPattern.test(rewritten) || !rewritten.includes(importLine)) {
    throw new Error('failed to rewrite response validators in sdk.gen.ts')
  }
  writeFileSync(sdkGenPath, rewritten)
}
