# @omnara/sdk

TypeScript SDK for the [Omnara](https://omnara.com) API.

## Installation

```sh
npm install @omnara/sdk
```

## Usage

```ts
import { createOmnaraClient, bearerToken } from '@omnara/sdk'

const client = createOmnaraClient({
  baseUrl: 'https://api.omnara.com',
  auth: bearerToken('your-api-key'),
})
```

### Entry points

- `@omnara/sdk` — Node client, auth, and event streaming
- `@omnara/sdk/browser` — browser-safe client
- `@omnara/sdk/tanstack` — TanStack Query hooks (requires the optional `@tanstack/react-query` peer dependency)
- `@omnara/sdk/zod` — Zod schemas for all API types

Requires Node.js 22 or later.

## Documentation

See the [Omnara docs](https://docs.omnara.com) and the [GitHub repository](https://github.com/omnara-ai/omnara).

## License

Apache-2.0
