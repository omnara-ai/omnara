export interface AuthStrategy {
  authenticate(request: Request): void | Promise<void>
}

type TokenResolver = () => string | Promise<string>

function isTokenResolver(token: string | TokenResolver): token is TokenResolver {
  return typeof token === 'function'
}

export function bearerToken(token: string | TokenResolver): AuthStrategy {
  const resolve = isTokenResolver(token) ? token : () => token
  return {
    async authenticate(request) {
      const value = await resolve()
      if (value) request.headers.set('Authorization', `Bearer ${value}`)
    },
  }
}
