import type { OmnaraClient } from '@omnara/sdk'
import { useMutation, type UseMutationOptions } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

type AnySdkFn<Data> = (options: {
  path: never
  body: never
  client?: OmnaraClient
}) => Promise<{ data: Data }>

type PathOf<TFn extends AnySdkFn<unknown>> = Parameters<TFn>[0]['path']
type BodyOf<TFn extends AnySdkFn<unknown>> = Parameters<TFn>[0]['body']

/**
 * Wrap a generated SDK mutation so path params bind once at hook scope and
 * the mutation variables are just the request body. The generated
 * `*Mutation()` builders can't do this split: their variables type is always
 * the full path+body envelope. Path, body, and response types are inferred
 * from the SDK function, so spec changes still surface at call sites.
 */
export function useScopedMutation<TFn extends AnySdkFn<Data>, Data>(
  fn: TFn & ((options: never) => Promise<{ data: Data }>),
  path: PathOf<TFn>,
  options?: Omit<UseMutationOptions<NonNullable<Data>, Error, BodyOf<TFn>>, 'mutationFn'>,
) {
  const client = useOmnaraClient()
  return useMutation({
    mutationFn: async (body: BodyOf<TFn>) => {
      const { data } = await fn({ path, body, client })
      if (data == null) throw new Error('The API returned an empty mutation response')
      return data
    },
    ...options,
  })
}
