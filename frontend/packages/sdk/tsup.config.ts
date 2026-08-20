import { defineConfig } from 'tsup'

export default defineConfig({
  entry: {
    index: 'src/index.ts',
    browser: 'src/browser.ts',
    tanstack: 'src/generated/@tanstack/react-query.gen.ts',
    zod: 'src/generated/zod.gen.ts',
  },
  format: ['esm'],
  dts: true,
  sourcemap: true,
  clean: true,
})
