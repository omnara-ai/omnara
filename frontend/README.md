# Frontend

User-facing clients and shared UI utilities live here:

- `web/` — Vite + React dashboard
- `mobile/` — Expo (React Native) app
- `packages/shared/` — design tokens + theme published as `@omnara/shared`

Wiring to know about:
- `web/vite.config.ts` maps `@omnara/shared` to the shared source for imports.
- `mobile/metro.config.js` watches `../packages` so Expo hot-reloads shared edits.
- Server code is maintained in `../backend` and `../servers`.
