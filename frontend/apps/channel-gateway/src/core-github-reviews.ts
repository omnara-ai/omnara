import {
  type ChannelConnectorEventReceipt,
  type createOmnaraClient,
  type LookupChannelConnectorGitHubReviewsRequest,
  type LookupChannelConnectorGitHubReviewsResponse,
  type RecordChannelConnectorGitHubReviewRequest,
  type RecordChannelConnectorGitHubReviewResponse,
  schemas,
  sdk,
} from '@omnara/sdk'

type Client = ReturnType<typeof createOmnaraClient>
export type GitHubReviewInstallation = Pick<
  ChannelConnectorEventReceipt,
  'integration_app_id' | 'integration_install_id'
>

export async function lookupGitHubReviews(
  client: Client,
  installation: GitHubReviewInstallation,
  request: LookupChannelConnectorGitHubReviewsRequest,
  signal: AbortSignal,
): Promise<LookupChannelConnectorGitHubReviewsResponse> {
  const { data } = await sdk.lookupChannelConnectorGitHubReviews({
    client,
    body: request,
    signal,
    redirect: 'error',
    path: {
      integrationAppID: installation.integration_app_id,
      integrationInstallID: installation.integration_install_id,
    },
  })
  // The public SDK tolerates future response enum values. Ownership is a private
  // dispatch decision and must use only a fully understood, correlated result.
  const parsed = schemas.zLookupChannelConnectorGitHubReviewsResponse.safeParse(data)
  if (!parsed.success || parsed.data.observations.length !== request.observations.length)
    throw new Error('Invalid GitHub review ownership response')
  for (const [index, observed] of parsed.data.observations.entries()) {
    const expected = request.observations[index]
    if (!expected || observed.review_id !== expected.review_id)
      throw new Error('Invalid GitHub review ownership response')
    if (observed.ownership === 'owned') {
      if (!observed.creating_tool_call_id || !observed.commit_id)
        throw new Error('Invalid GitHub review ownership response')
    } else if (observed.creating_tool_call_id !== undefined || observed.commit_id !== undefined) {
      throw new Error('Invalid GitHub review ownership response')
    }
  }
  return parsed.data
}

export async function recordGitHubReview(
  client: Client,
  installation: GitHubReviewInstallation,
  request: RecordChannelConnectorGitHubReviewRequest,
  signal: AbortSignal,
): Promise<RecordChannelConnectorGitHubReviewResponse> {
  const { data } = await sdk.recordChannelConnectorGitHubReview({
    client,
    body: request,
    signal,
    redirect: 'error',
    path: {
      integrationAppID: installation.integration_app_id,
      integrationInstallID: installation.integration_install_id,
    },
  })
  const parsed = schemas.zRecordChannelConnectorGitHubReviewResponse.safeParse(data)
  if (!parsed.success || !parsed.data.recorded)
    throw new Error('Invalid GitHub review identity acknowledgment')
  return parsed.data
}
