# Omnara Web — Agent Command Center

A React + Vite + TypeScript web app for monitoring and managing AI agents. Uses Tailwind CSS and shared design tokens from the monorepo.

## Features
- React + Vite (fast dev/build)
- Tailwind CSS utilities and component primitives
- Supabase client for auth/data
- PostHog analytics

## Requirements
- Node.js 18+ and npm

## Quick Start
1. From `frontend/web`, copy env and fill publishable values:
   - `cp .env.example .env`
   - Set `VITE_API_URL`, `VITE_SUPABASE_URL`, `VITE_SUPABASE_ANON_KEY`
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
- `VITE_POSTHOG_KEY` — Optional PostHog public key
- `VITE_POSTHOG_HOST` — Optional PostHog host (defaults to US)

## Build & Deploy
- Build: `npm run build` (outputs `dist/`)
- Deploy the `dist/` folder to any static host (Netlify, Vercel, S3, etc.)

## License
See `LICENSE` [here](https://github.com/omnara-ai/omnara/blob/main/frontend/LICENSE).
