import { readFileSync, writeFileSync } from 'node:fs'
import { URL } from 'node:url'

import ts from 'typescript'

// Inferred TanStack query keys emit unnameable unique symbols in consumer declarations.
const path = new URL('../src/generated/@tanstack/react-query.gen.ts', import.meta.url)
const source = readFileSync(path, 'utf8')
const file = ts.createSourceFile(path.pathname, source, ts.ScriptTarget.Latest, true)
const edits = []
let helpers = 0
for (const statement of file.statements) {
  if (!ts.isVariableStatement(statement)) continue
  for (const declaration of statement.declarationList.declarations) {
    const fn = declaration.initializer
    if (!fn || !ts.isArrowFunction(fn)) continue
    const calls = []
    const visit = (node) => {
      if (
        ts.isCallExpression(node) &&
        ts.isIdentifier(node.expression) &&
        ['queryOptions', 'infiniteQueryOptions'].includes(node.expression.text)
      )
        calls.push(node)
      ts.forEachChild(node, visit)
    }
    visit(fn.body)
    if (calls.length === 0) continue
    if (calls.length !== 1) throw new Error('Unexpected generated TanStack options helper')
    helpers++
    if (fn.type) continue
    const [call] = calls
    const infinite = call.expression.text === 'infiniteQueryOptions'
    const settings = call.arguments[0]
    const queryFn =
      settings && ts.isObjectLiteralExpression(settings)
        ? settings.properties.find(
            (property) =>
              ts.isPropertyAssignment(property) && property.name.getText(file) === 'queryFn',
          )
        : undefined
    if (!queryFn || !ts.isArrowFunction(queryFn.initializer)) {
      throw new Error('Generated query options must supply a function, never skipToken')
    }
    const args = call.typeArguments?.map((argument) => argument.getText(file))
    if (!args || args.length !== (infinite ? 5 : 4)) {
      throw new Error('Unexpected generated TanStack generic arguments')
    }
    if (infinite) {
      if (
        !ts.isBlock(fn.body) ||
        fn.body.statements.at(-1)?.getText(file) !==
          "return opts as Omit<typeof opts, 'initialData'>;"
      ) {
        throw new Error('Unexpected generated infinite-query return; review its public type')
      }
    } else if (fn.body !== call) {
      throw new Error('Unexpected generated query-options return; review its public type')
    }
    const [data, error, , key] = args
    const options = `${infinite ? 'UnusedSkipTokenInfiniteOptions' : 'UnusedSkipTokenOptions'}<${args.join(', ')}>`
    const tagged = `${options} & { queryKey: DataTag<${key}, ${infinite ? `InfiniteData<${data}>` : data}, ${error}> }`
    const result = infinite ? `Omit<${tagged}, 'initialData'>` : tagged
    const position = fn.equalsGreaterThanToken.getStart(file) - 1
    if (source[position] !== ' ') throw new Error('Unexpected generated arrow-function format')
    edits.push({ position, end: position, text: `: ${result}` })
    if (infinite) {
      const returned = fn.body.statements.at(-1)
      edits.push({
        position: returned.getStart(file),
        end: returned.end,
        text: `return opts as ${result};`,
      })
    }
  }
}
if (helpers === 0) throw new Error('No generated TanStack query helpers found')
if (edits.length > 0) {
  let rewritten = source
  for (const { position, end, text } of edits.sort((a, b) => b.position - a.position)) {
    rewritten = rewritten.slice(0, position) + text + rewritten.slice(end)
  }
  const importLine =
    "import type { DataTag, UnusedSkipTokenInfiniteOptions, UnusedSkipTokenOptions } from '@tanstack/react-query';\n"
  const firstImport = file.statements.find(ts.isImportDeclaration)
  if (!firstImport) throw new Error('No generated TanStack imports found')
  const position = firstImport.getStart(file)
  rewritten = rewritten.slice(0, position) + importLine + rewritten.slice(position)
  writeFileSync(path, rewritten)
}
