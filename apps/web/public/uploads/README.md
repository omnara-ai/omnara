This folder holds large/static marketing assets used by the web app (videos, screenshots, backgrounds).

Repository policy:
- This directory is intentionally gitignored to keep the OSS repo lean and avoid licensing/PII issues.
- Add only minimal, publishable assets for local development if needed.

Recommended structure:
- backgrounds/phone_bg_2/*  (AVIF/WebP/JPG) — small demo background
- phone-shots/screen-one|two|three/* — optional screenshots (compressed)

Notes:
- Prefer AVIF/WebP with a small JPG fallback.
- Keep individual files under ~300–500 KB when possible.
- If deploying a marketing site, host heavy assets on a CDN (e.g., S3/Supabase Storage) and point the app to those URLs.

This file exists to keep the directory in git history without committing large binaries.
