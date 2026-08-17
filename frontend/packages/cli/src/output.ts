import * as z from 'zod'
import { ApiError } from '@omnara/sdk'

export type RenderValue = string | number | boolean | null | undefined | void | object

const PREFERRED_LEADING_COLUMNS = ['id', 'name']
const HIDDEN_TABLE_COLUMNS = new Set(['org_id', 'project_id'])

type Row = Record<string, RenderValue>

const useColor =
  process.stdout.isTTY === true && process.env.NO_COLOR === undefined

function styled(code: string, text: string): string {
  return useColor ? `\u001b[${code}m${text}\u001b[0m` : text
}

function header(text: string): string {
  return styled('1', text)
}

function dim(text: string): string {
  return styled('2', text)
}

function isScalar(value: RenderValue): value is string | number | boolean | null {
  return (
    value === null ||
    typeof value === 'string' ||
    typeof value === 'number' ||
    typeof value === 'boolean'
  )
}

function isRow(value: RenderValue): value is Row {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function isRowArray(value: RenderValue): value is Row[] {
  return Array.isArray(value) && value.length > 0 && value.every(isRow)
}

function cell(value: RenderValue): string {
  if (value === null || value === undefined || value === '') return '–'
  if (typeof value === 'string') return formatTimestamp(value)
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

const TIMESTAMP_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/

function formatTimestamp(value: string): string {
  if (!TIMESTAMP_PATTERN.test(value)) return value
  return value.replace('T', ' ').replace(/\.\d+(Z|[+-]\d{2}:\d{2})$/, '$1')
}

function indented(indent: number, line: string): string {
  return `${' '.repeat(indent)}${line}`
}

function tableColumns(rows: Row[]): string[] {
  const keys = new Set<string>()
  for (const row of rows) {
    for (const [key, value] of Object.entries(row)) {
      if (isScalar(value) && !HIDDEN_TABLE_COLUMNS.has(key)) keys.add(key)
    }
  }
  return [...keys].sort((a, b) => {
    const rankA = PREFERRED_LEADING_COLUMNS.indexOf(a)
    const rankB = PREFERRED_LEADING_COLUMNS.indexOf(b)
    if (rankA !== -1 || rankB !== -1) {
      return (rankA === -1 ? PREFERRED_LEADING_COLUMNS.length : rankA) -
        (rankB === -1 ? PREFERRED_LEADING_COLUMNS.length : rankB)
    }
    return 0
  })
}

const COLUMN_GAP = 2
const MIN_COLUMN_WIDTH = 6

function terminalWidth(): number | undefined {
  if (process.stdout.isTTY) return process.stdout.columns
  const fromEnv = Number(process.env.COLUMNS)
  return Number.isInteger(fromEnv) && fromEnv > 0 ? fromEnv : undefined
}

function fitWidths(
  naturalWidths: number[],
  available: number,
  shrinkable: (index: number) => boolean,
): number[] {
  const widths = [...naturalWidths]
  const totalWidth = () =>
    widths.reduce((sum, width) => sum + width, 0) + COLUMN_GAP * (widths.length - 1)
  while (totalWidth() > available) {
    let widest = -1
    for (let index = 0; index < widths.length; index++) {
      if (!shrinkable(index) || (widths[index] ?? 0) <= MIN_COLUMN_WIDTH) continue
      if (widest === -1 || (widths[index] ?? 0) > (widths[widest] ?? 0)) widest = index
    }
    if (widest === -1) break
    widths[widest] = (widths[widest] ?? 0) - 1
  }
  return widths
}

function truncate(value: string, width: number): string {
  if (value.length <= width) return value
  return `${value.slice(0, Math.max(0, width - 1))}…`
}

function printTable(rows: Row[], indent: number, explicitColumns?: readonly string[]): void {
  if (rows.length === 0) {
    console.log(indented(indent, dim('(no results)')))
    return
  }
  const columns = [...(explicitColumns ?? tableColumns(rows))]
  const naturalWidths = columns.map((column) =>
    Math.max(column.length, ...rows.map((row) => cell(row[column]).length)),
  )
  const available = terminalWidth()
  const widths =
    available === undefined
      ? naturalWidths
      : fitWidths(naturalWidths, available - indent, (index) => columns[index] !== 'id')
  const pad = (value: string, index: number) =>
    truncate(value, widths[index] ?? 0).padEnd(widths[index] ?? 0)
  console.log(
    indented(
      indent,
      columns.map((column, index) => header(pad(column.toUpperCase(), index))).join('  '),
    ),
  )
  for (const row of rows) {
    console.log(
      indented(
        indent,
        columns.map((column, index) => pad(cell(row[column]), index)).join('  ').trimEnd(),
      ),
    )
  }
}

function printObject(value: Row, indent: number): void {
  const entries = Object.entries(value)
  const scalarEntries = entries.filter(([, field]) => isScalar(field))
  const nestedEntries = entries.filter(([, field]) => !isScalar(field))
  const width = Math.max(0, ...scalarEntries.map(([key]) => key.length))
  for (const [key, field] of scalarEntries) {
    console.log(indented(indent, `${dim(key.padEnd(width))}  ${cell(field)}`))
  }
  for (const [key, field] of nestedEntries) {
    console.log(indented(indent, header(`${key}:`)))
    printValue(field, indent + 2)
  }
}

function printValue(value: RenderValue, indent: number, columns?: readonly string[]): void {
  if (isRowArray(value)) {
    printTable(value, indent, columns)
    return
  }
  if (Array.isArray(value)) {
    if (value.length === 0) {
      console.log(indented(indent, dim('(none)')))
      return
    }
    for (const item of value) console.log(indented(indent, cell(item)))
    return
  }
  if (isRow(value)) {
    printObject(value, indent)
    return
  }
  console.log(indented(indent, cell(value)))
}

function isListEnvelope(value: Row): value is Row & { data: RenderValue[]; next_cursor: string | null } {
  const keys = Object.keys(value)
  return (
    keys.length === 2 &&
    keys.includes('data') &&
    keys.includes('next_cursor') &&
    Array.isArray(value.data)
  )
}

export interface RenderOptions {
  columns?: readonly string[]
}

export function renderResult(data: RenderValue, asJson: boolean, options: RenderOptions = {}): void {
  if (asJson) {
    console.log(JSON.stringify(data ?? null, null, 2))
    return
  }
  if (data === undefined || data === null || data === '') {
    console.log('ok')
    return
  }
  if (isRow(data) && isListEnvelope(data)) {
    if (isRowArray(data.data) || data.data.length === 0) {
      printTable(data.data as Row[], 0, options.columns)
    } else {
      printValue(data.data, 0, options.columns)
    }
    if (typeof data.next_cursor === 'string') {
      console.log(dim(`(more results: pass --all, or --cursor ${data.next_cursor})`))
    }
    return
  }
  printValue(data, 0, options.columns)
}

export class CliInputError extends Error {}

export async function runCliAction(action: () => Promise<void>): Promise<void> {
  try {
    await action()
  } catch (error) {
    if (error instanceof CliInputError) {
      console.error(`error: ${error.message}`)
    } else if (error instanceof z.ZodError) {
      console.error(`error: unexpected response shape:\n${z.prettifyError(error)}`)
    } else if (error instanceof ApiError) {
      const code = error.code ? ` [${error.code}]` : ''
      console.error(`error: API ${error.status}${code}: ${error.message}`)
    } else {
      console.error(`error: ${error instanceof Error ? error.message : String(error)}`)
    }
    process.exitCode = 1
  }
}
