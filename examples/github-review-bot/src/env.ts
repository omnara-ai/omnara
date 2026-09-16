import { z } from 'zod'

export const zBindings = z.object({
  PUBLIC_URL: z.url(),
  OMNARA_API_KEY: z.string().min(1),
  OMNARA_API_URL: z.url().default('https://api.omnara.com/v1'),
  OMNARA_ORG_ID: z.string().min(1),
  OMNARA_PROJECT_ID: z.string().min(1),
  GITHUB_APP_ID: z.string().min(1),
  GITHUB_APP_PRIVATE_KEY: z
    .string()
    .min(1)
    .transform((pem) => pem.replaceAll('\\n', '\n')),
  GITHUB_WEBHOOK_SECRET: z.string().min(1),
  REVIEW_JWT_SECRET: z.string().min(32),
})
export type Bindings = z.infer<typeof zBindings>
