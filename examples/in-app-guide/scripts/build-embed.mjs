#!/usr/bin/env node
// Bundles the guide widget (React included) into one script, for apps that aren't
// built with React and for trying the guide through try/proxy.ts.
//
//   node scripts/build-embed.mjs [--out embed.js] [--dev]
//
// --dev keeps the window.__omnaraGuide hook that browser tests use to drive the widget.

import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const here = path.dirname(path.dirname(fileURLToPath(import.meta.url)))
const dev = process.argv.includes('--dev')
const i = process.argv.indexOf('--out')
const outfile = path.resolve(i > 0 ? process.argv[i + 1] : 'embed.js')

const result = await build({
  entryPoints: [path.join(here, 'guide/embed.tsx')],
  outfile,
  bundle: true,
  format: 'iife',
  platform: 'browser',
  target: ['chrome120', 'safari17', 'firefox120'],
  jsx: 'automatic',
  minify: !dev,
  sourcemap: dev ? 'inline' : false,
  define: { 'process.env.NODE_ENV': JSON.stringify(dev ? 'development' : 'production') },
  metafile: true,
  logLevel: 'warning',
})
const out = Object.entries(result.metafile.outputs)[0]
console.log(`wrote ${path.relative(process.cwd(), out[0])} (${(out[1].bytes / 1024).toFixed(0)} KB${dev ? ', dev' : ''})`)
