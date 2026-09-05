import { createAppAuth, type InstallationAuthOptions } from '@octokit/auth-app'
import { z } from 'zod'
import type { Bindings } from './env'

export const REVIEW_TOKEN_TTL = '7d'
export const REVIEW_TOKEN_ISSUER = 'omnara-github-review-bot'

export const zReviewClaims = z.object({
  repo: z.string().regex(/^[\w.-]+\/[\w.-]+$/),
  pr: z.number().int().positive(),
  installation_id: z.number().int().positive(),
})
export type ReviewClaims = z.infer<typeof zReviewClaims>

type InstallationPermissions = NonNullable<InstallationAuthOptions['permissions']>

export async function installationToken(
  env: Bindings,
  claims: ReviewClaims,
  permissions: InstallationPermissions,
): Promise<{ token: string; expires_at: string }> {
  const auth = createAppAuth({ appId: env.GITHUB_APP_ID, privateKey: env.GITHUB_APP_PRIVATE_KEY })
  const [, repositoryName] = claims.repo.split('/')
  const installation = await auth({
    type: 'installation',
    installationId: claims.installation_id,
    repositoryNames: [repositoryName],
    permissions,
  })
  return { token: installation.token, expires_at: installation.expiresAt }
}
