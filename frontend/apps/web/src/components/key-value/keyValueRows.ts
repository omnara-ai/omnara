export interface TextRow {
  id: string
  key: string
  /** null is an unset entry: the overlay removes the inherited value for this key. */
  value: string | null
}

export interface SecretRow {
  id: string
  key: string
  /** null is an unset entry: the overlay removes the inherited value for this key. */
  secretId: string | null
}

export function newTextRow(): TextRow {
  return { id: crypto.randomUUID(), key: '', value: '' }
}

function newSecretRow(): SecretRow {
  return { id: crypto.randomUUID(), key: '', secretId: '' }
}

export function textRowsFromRecord(values: Record<string, string | null>): TextRow[] {
  return Object.entries(values)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, value]) => ({ ...newTextRow(), key, value }))
}

export function secretRowsFromRecord(values: Record<string, string | null>): SecretRow[] {
  return Object.entries(values)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, secretId]) => ({ ...newSecretRow(), key, secretId }))
}

function rowKeysValid(rows: { key: string }[]) {
  const keys = rows.map((row) => row.key.trim())
  return keys.every((key) => key !== '') && new Set(keys).size === keys.length
}

export function textRowsValid(rows: TextRow[]) {
  return rowKeysValid(rows)
}

export function secretRowsValid(rows: SecretRow[]) {
  return rowKeysValid(rows) && rows.every((row) => row.secretId !== '')
}

export function recordFromTextRows(rows: TextRow[]): Record<string, string> | undefined {
  if (rows.length === 0) return undefined
  return Object.fromEntries(rows.map((row) => [row.key.trim(), row.value ?? '']))
}

export function recordFromSecretRows(rows: SecretRow[]): Record<string, string> | undefined {
  if (rows.length === 0) return undefined
  return Object.fromEntries(rows.map((row) => [row.key.trim(), row.secretId ?? '']))
}

export function overlayFromTextRows(rows: TextRow[]): Record<string, string | null> | undefined {
  if (rows.length === 0) return undefined
  return Object.fromEntries(rows.map((row) => [row.key.trim(), row.value]))
}

export function overlayFromSecretRows(
  rows: SecretRow[],
): Record<string, string | null> | undefined {
  if (rows.length === 0) return undefined
  return Object.fromEntries(rows.map((row) => [row.key.trim(), row.secretId]))
}
