import * as z from 'zod'

export const zJsonBody = z.json()

export type JsonBody = z.infer<typeof zJsonBody>

export const zJsonText = z.string().transform((text, ctx) => {
  try {
    const value: unknown = JSON.parse(text)
    return value
  } catch (error) {
    ctx.addIssue({ code: 'custom', message: String(error), input: text, params: { cause: error } })
    return z.NEVER
  }
})
