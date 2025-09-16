# Omnara Web — Agent Command Center

A React + Vite + TypeScript web app for monitoring and managing AI agents. Uses Tailwind CSS and shared design tokens from the monorepo.

## Features
- React + Vite (fast dev/build)
- Tailwind CSS utilities and component primitives
- Supabase client (publishable anon key) for auth/data
- Optional PostHog analytics
- Shared tokens via `@omnara/shared` (monorepo package)

## Requirements
- Node.js 18+ and npm

## Quick Start
1. From `frontend/web`, copy env and fill publishable values:
   - `cp .env.example .env`
   - Set `VITE_API_URL`, `VITE_SUPABASE_URL`, `VITE_SUPABASE_ANON_KEY`
   - Set `VITE_PUBLIC_ASSETS_BASE_URL` for CDN/Storage-hosted assets
   - Optionally set `VITE_POSTHOG_KEY` and `VITE_POSTHOG_HOST`
2. Install and run:
   - `npm install`
   - `npm run dev`

## Scripts
- `npm run dev` — Start Vite dev server
- `npm run build` — Production build to `dist/`
- `npm run preview` — Preview built app
- `npm run lint` — Lint source

## Env Vars (frontend only)
- `VITE_API_URL` — Backend base URL
- `VITE_SUPABASE_URL` — Supabase project URL
- `VITE_SUPABASE_ANON_KEY` — Supabase anon (publishable) key
- `VITE_PUBLIC_ASSETS_BASE_URL` — Base URL for public assets (e.g., Supabase Storage public bucket)
- `VITE_POSTHOG_KEY` — Optional PostHog public key
- `VITE_POSTHOG_HOST` — Optional PostHog host (defaults to US)

Never commit secrets here. Only publishable keys belong in `VITE_` variables. Keep server secrets on the backend.

## Monorepo Notes
- Shared package: `frontend/packages/shared`
- Import shared tokens via: `import { tokens, colors } from '@omnara/shared'`
- Path aliases:
  - `@` → `./src`
  - `@omnara/shared` → `../packages/shared/src`

Hot reloading of `@omnara/shared` works during dev. The Vite config is set to allow reading from the monorepo `packages` path.

## Build & Deploy
- Build: `npm run build` (outputs `dist/`)
- Deploy the `dist/` folder to any static host (Netlify, Vercel, S3, etc.)
- Security headers are provided in `public/_headers` (clickjacking protection). Ensure equivalent headers in production.

## License
See `LICENSE` in the repository.
