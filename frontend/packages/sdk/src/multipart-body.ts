import * as z from 'zod'

import type { BodySerializer } from './generated/core/bodySerializer.gen'

const zMultipartPart = z.union([
  z.string(),
  z.instanceof(Blob),
  z.date().transform((date) => date.toISOString()),
  z.union([z.number(), z.boolean(), z.bigint()]).transform(String),
  z.json().transform((value) => new Blob([JSON.stringify(value)], { type: 'application/json' })),
])

const zMultipartBody = z.record(
  z.string(),
  z.union([z.array(zMultipartPart), zMultipartPart]).nullish(),
)

export const serializeMultipartBody = ((body) => {
  const form = new FormData()
  for (const [key, value] of Object.entries(zMultipartBody.parse(body))) {
    if (value === null || value === undefined) continue
    for (const part of Array.isArray(value) ? value : [value]) form.append(key, part)
  }
  return form
}) satisfies BodySerializer
