import type { OmnaraClient } from '@omnara/sdk'
import { useMutation, type UseMutationOptions } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

type AnySdkFn = (options: {
  path: never
  body: never
  client?: OmnaraClient
}) => Promise<{ data: unknown }>

type PathOf<TFn extends AnySdkFn> = Parameters<TFn>[0]['path']
type BodyOf<TFn extends AnySdkFn> = Parameters<TFn>[0]['body']
// The client is configured with throwOnError, so data is always present.
type DataOf<TFn extends AnySdkFn> = NonNullable<Awaited<ReturnType<TFn>>['data']>

/**
 * Wrap a generated SDK mutation so path params bind once at hook scope and
 * the mutation variables are just the request body. The generated
 * `*Mutation()` builders can't do this split: their variables type is always
 * the full path+body envelope. Path, body, and response types are inferred
 * from the SDK function, so spec changes still surface at call sites.
 */
export function useScopedMutation<TFn extends AnySdkFn>(
  fn: TFn,
  path: PathOf<TFn>,
  options?: Omit<UseMutationOptions<DataOf<TFn>, Error, BodyOf<TFn>>, 'mutationFn'>,
) {
  const client = useOmnaraClient()
  return useMutation({
    mutationFn: async (body: BodyOf<TFn>) => {
      const { data } = await fn({ path, body, client })
      return data as DataOf<TFn>
    },
    ...options,
  })
}
