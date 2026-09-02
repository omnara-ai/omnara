import type { Query } from '@tanstack/react-query'
import { z } from 'zod'

const generatedQueryKeyEntry = z.object({
  _id: z.string(),
  path: z.object({ orgID: z.string().optional(), projectID: z.string().optional() }).optional(),
})

export type GeneratedQueryKeyEntry = z.infer<typeof generatedQueryKeyEntry>

export function generatedQueryKey(query: Query): GeneratedQueryKeyEntry | undefined {
  const parsed = generatedQueryKeyEntry.safeParse(query.queryKey[0])
  return parsed.success ? parsed.data : undefined
}
