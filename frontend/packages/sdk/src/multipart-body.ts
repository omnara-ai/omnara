// OpenAPI defaults object-valued multipart properties to application/json. The
// generated serializer appends them as untyped text instead, which the API's
// request validator reads as text/plain and rejects (for example `owner` on
// createSkill).
export function serializeMultipartBody(body: unknown): FormData {
  const form = new FormData()
  for (const [key, value] of Object.entries(body as Record<string, unknown>)) {
    if (value === undefined || value === null) continue
    for (const item of Array.isArray(value) ? value : [value]) appendPart(form, key, item)
  }
  return form
}

function appendPart(form: FormData, key: string, value: unknown): void {
  if (typeof value === 'string' || value instanceof Blob) {
    form.append(key, value)
  } else if (value instanceof Date) {
    form.append(key, value.toISOString())
  } else if (typeof value === 'object' && value !== null) {
    form.append(key, new Blob([JSON.stringify(value)], { type: 'application/json' }))
  } else {
    form.append(key, String(value))
  }
}
