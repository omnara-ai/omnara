import { defineConfig } from "vite";
import react from "@vitejs/plugin-react-swc";
import path from "path";
import { componentTagger } from "lovable-tagger";
import { sentryVitePlugin } from "@sentry/vite-plugin";

// https://vitejs.dev/config/
export default defineConfig(({ mode }) => ({
  server: {
    host: "::",
    port: 5173,
    headers: {
      'X-Frame-Options': 'DENY',
      'Content-Security-Policy': "frame-ancestors 'none'",
    },
    fs: {
      // allow serving files from the shared package during dev
      allow: [
        path.resolve(__dirname, '..'), // frontend/
        path.resolve(__dirname, '../packages/shared'),
      ],
    },
  },
  plugins: [
    react(),
    mode === 'development' && componentTagger(),
    process.env.SENTRY_AUTH_TOKEN &&
      sentryVitePlugin({
        authToken: process.env.SENTRY_AUTH_TOKEN,
        org: "omnara",
        project: "web-app",
      }),
  ].filter(Boolean),
  resolve: {
    preserveSymlinks: true,
    alias: {
      "@": path.resolve(__dirname, "./src"),
      // shared tokens used by both web and mobile
      "@omnara/shared": path.resolve(__dirname, "../packages/shared/src"),
    },
  },
  build: {
    // Enable code splitting and lazy loading
    rollupOptions: {
      output: {
        manualChunks: {
          'react-vendor': ['react', 'react-dom', 'react-router-dom'],
          'ui-vendor': ['@radix-ui/react-dialog', '@radix-ui/react-dropdown-menu', '@radix-ui/react-tooltip'],
          'chart-vendor': ['recharts'],
          'date-vendor': ['date-fns'],
        },
      },
    },
    // Optimize chunk size
    chunkSizeWarningLimit: 1000,
    // Enable source maps for better debugging
    sourcemap: true,
    // Minify for production (using default esbuild which is faster)
    minify: mode === 'production' ? 'esbuild' : false,
  },
  // Optimize dependencies
  optimizeDeps: {
    include: ['react', 'react-dom', 'react-router-dom'],
    // don't prebundle the shared source so edits hot-reload cleanly
    exclude: ['@omnara/shared'],
  },
}));
