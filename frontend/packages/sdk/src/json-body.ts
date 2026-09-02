import * as z from 'zod'

export const zJsonBody = z.json()

export type JsonBody = z.infer<typeof zJsonBody>

export async function jsonBody(response: Response): Promise<JsonBody | undefined> {
  try {
    return zJsonBody.parse(await response.clone().json())
  } catch {
    return undefined
  }
}
