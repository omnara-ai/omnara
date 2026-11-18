import { z } from 'zod'

const part = z.union([
  z.object({ type: z.literal('text'), content: z.string() }),
  z
    .object({ type: z.literal('tool_call'), name: z.string(), arguments: z.unknown() })
    .transform((call) => ({
      type: 'tool_call' as const,
      name: call.name,
      arguments: JSON.stringify(call.arguments),
    })),
  // Any other part, such as a tool result or reasoning, is shown as it was recorded.
  z
    .record(z.string(), z.unknown())
    .transform((raw) => ({ type: 'other' as const, json: JSON.stringify(raw, null, 2) })),
])

const messages = z.array(z.object({ role: z.string(), parts: z.array(part) })).min(1)

export type TraceMessage = z.infer<typeof messages>[number]

/** Reads a GenAI message list, such as gen_ai.input.messages. Undefined for any other value. */
export function parseMessages(value: string): TraceMessage[] | undefined {
  if (!value.trimStart().startsWith('[')) return undefined
  try {
    const parsed = messages.safeParse(JSON.parse(value))
    return parsed.success ? parsed.data : undefined
  } catch {
    return undefined
  }
}
