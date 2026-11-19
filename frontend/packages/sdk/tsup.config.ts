import { defineConfig } from 'tsup'

export default defineConfig({
  entry: {
    index: 'src/index.ts',
    browser: 'src/browser.ts',
    tanstack: 'src/tanstack.ts',
    zod: 'src/generated/zod.gen.ts',
    'config-source': 'src/config-source.ts',
  },
  format: ['esm'],
  dts: true,
  sourcemap: true,
  clean: true,
})
