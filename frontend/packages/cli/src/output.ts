import { ApiError } from '@omnara/sdk'
import * as z from 'zod'

const zRenderValue = z.json()
export type RenderValue = z.output<typeof zRenderValue>
export const zOptionalRenderValue = zRenderValue.optional()

type Scalar = string | number | boolean | null
type Row = Record<string, RenderValue>
const zListEnvelope = z.strictObject({
  data: z.array(zRenderValue),
  next_cursor: z.string().nullable(),
})

const PREFERRED_LEADING_COLUMNS = ['id', 'name']
const HIDDEN_TABLE_COLUMNS = new Set(['org_id', 'project_id'])

const useColor = process.stdout.isTTY && process.env.NO_COLOR === undefined

function styled(code: string, text: string): string {
  return useColor ? `\u001b[${code}m${text}\u001b[0m` : text
}

function header(text: string): string {
  return styled('1', text)
}

function dim(text: string): string {
  return styled('2', text)
}

function isScalar(value: RenderValue | undefined): value is Scalar | undefined {
  return typeof value !== 'object' || value === null
}

function isRow(value: RenderValue): value is Row {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function objectRows(values: RenderValue[]): Row[] | undefined {
  const rows = values.flatMap((value) => (isRow(value) ? [value] : []))
  return rows.length === values.length ? rows : undefined
}

function cell(value: RenderValue | undefined): string {
  if (value === null || value === undefined || value === '') return '–'
  return isScalar(value) ? formatTimestamp(String(value)) : JSON.stringify(value)
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
      return (
        (rankA === -1 ? PREFERRED_LEADING_COLUMNS.length : rankA) -
        (rankB === -1 ? PREFERRED_LEADING_COLUMNS.length : rankB)
      )
    }
    return 0
  })
}

const COLUMN_GAP = 2
const MIN_COLUMN_WIDTH = 6

function terminalWidth(): number | undefined {
  if (!process.stdout.isTTY) return undefined
  const columns = process.stdout.columns
  return Number.isInteger(columns) && columns > 0 ? columns : undefined
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
  let end = Math.max(0, width - 1)
  const lastUnit = value.charCodeAt(end - 1)
  if (end > 0 && lastUnit >= 0xd800 && lastUnit <= 0xdbff) end -= 1
  return `${value.slice(0, end)}…`
}

export function abbreviate(text: string, max: number): string {
  return truncate(text.replaceAll(/\s+/g, ' ').trim(), max)
}

function printTable(rows: Row[], indent: number, explicitColumns?: readonly string[]): void {
  if (rows.length === 0) {
    console.log(indented(indent, dim('(no results)')))
    return
  }
  const columns = [...(explicitColumns ?? tableColumns(rows))]
  const naturalWidths = columns.map((column) =>
    rows.reduce((width, row) => Math.max(width, cell(row[column]).length), column.length),
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
      columns
        .map((column, index) => header(pad(column.toUpperCase(), index)))
        .join('  ')
        .trimEnd(),
    ),
  )
  for (const row of rows) {
    console.log(
      indented(
        indent,
        columns
          .map((column, index) => pad(cell(row[column]), index))
          .join('  ')
          .trimEnd(),
      ),
    )
  }
}

function printObject(value: Row, indent: number): void {
  const entries = Object.entries(value)
  const scalarEntries = entries.filter(([, field]) => isScalar(field))
  const nestedEntries = entries.filter(([, field]) => !isScalar(field))
  const width = scalarEntries.reduce((max, [key]) => Math.max(max, key.length), 0)
  for (const [key, field] of scalarEntries) {
    console.log(indented(indent, `${dim(key.padEnd(width))}  ${cell(field)}`))
  }
  for (const [key, field] of nestedEntries) {
    console.log(indented(indent, header(`${key}:`)))
    printValue(field, indent + 2)
  }
}

function printValue(value: RenderValue, indent: number, columns?: readonly string[]): void {
  if (Array.isArray(value)) {
    if (value.length === 0) {
      console.log(indented(indent, dim('(none)')))
      return
    }
    const rows = objectRows(value)
    if (rows !== undefined) {
      printTable(rows, indent, columns)
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

export interface RenderOptions {
  columns?: readonly string[]
}

export function renderResult(
  value: RenderValue | undefined,
  asJson: boolean,
  options: RenderOptions = {},
): void {
  if (asJson) {
    console.log(JSON.stringify(value ?? null, null, 2))
    return
  }
  if (value === undefined || value === null || value === '') {
    console.log('ok')
    return
  }
  const envelope = zListEnvelope.safeParse(value)
  if (envelope.success) {
    const rows = objectRows(envelope.data.data)
    if (rows !== undefined) {
      printTable(rows, 0, options.columns)
    } else {
      printValue(envelope.data.data, 0, options.columns)
    }
    if (envelope.data.next_cursor !== null) {
      console.log(dim(`(more results: pass --all, or --cursor ${envelope.data.next_cursor})`))
    }
    return
  }
  printValue(value, 0, options.columns)
}

export class CliInputError extends Error {}

export async function runCliAction(action: () => void | Promise<void>): Promise<void> {
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
