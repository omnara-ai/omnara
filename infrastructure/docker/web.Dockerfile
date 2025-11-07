# Build stage
FROM node:20-alpine AS builder

WORKDIR /app

# Copy package files
COPY apps/web/package.json apps/web/package-lock.json ./

# Install dependencies
RUN npm ci

# Copy shared package (needed for theme imports)
COPY apps/packages/shared/ ../packages/shared/

# Copy application code
COPY apps/web/ ./

# Build arguments for environment variables
ARG VITE_SUPABASE_URL
ARG VITE_SUPABASE_ANON_KEY
ARG VITE_API_URL
ARG VITE_RELAY_URL
ARG VITE_SENTRY_DSN
ARG VITE_POSTHOG_KEY
ARG VITE_POSTHOG_HOST

# Build the application
RUN npm run build

# Production stage
FROM nginx:alpine

# Copy custom nginx config
COPY infrastructure/nginx/web.conf /etc/nginx/conf.d/default.conf

# Copy built assets from builder
COPY --from=builder /app/dist /usr/share/nginx/html

# Add healthcheck
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:80/ || exit 1

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
