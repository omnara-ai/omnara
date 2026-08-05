export interface AuthStrategy {
  authenticate(request: Request): void | Promise<void>
}

export function bearerToken(token: string | (() => string | Promise<string>)): AuthStrategy {
  const resolve = typeof token === 'function' ? token : () => token
  return {
    async authenticate(request) {
      const value = await resolve()
      if (value) request.headers.set('Authorization', `Bearer ${value}`)
    },
  }
}
