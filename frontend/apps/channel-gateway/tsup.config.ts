import { defineConfig } from 'tsup'

export default defineConfig({
  clean: true,
  dts: true,
  entry: ['src/index.ts'],
  format: ['esm'],
  noExternal: ['@omnara/sdk'],
  platform: 'node',
  target: 'node24',
})
