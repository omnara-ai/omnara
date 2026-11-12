// The guide as one script, for apps that aren't built with React. Bundle this file (it
// brings its own React) and load it from your app's own origin:
//
//   npx esbuild guide/embed.tsx --bundle --minify --outfile=public/guide.js
//
//   <script src="/guide.js" data-endpoint="/api/guide" data-title="Ask Acme"
//           data-accent="#4f46e5" data-show-only="true"></script>
//
// or call window.OmnaraGuide.mount({ endpoint, … }) yourself.

import { createRoot, type Root } from 'react-dom/client'
import { Guide, type GuideProps } from './Guide'

declare global {
  interface Window {
    OmnaraGuide?: { mount: (props: GuideProps) => void; unmount: () => void }
  }
}

let root: Root | null = null
let container: HTMLElement | null = null

function mount(props: GuideProps) {
  unmount()
  container = document.createElement('div')
  container.setAttribute('data-omnara-guide-root', '')
  document.body.append(container)
  root = createRoot(container)
  root.render(<Guide {...props} />)
}

function unmount() {
  root?.unmount()
  container?.remove()
  root = null
  container = null
}

window.OmnaraGuide = { mount, unmount }

const script = document.currentScript as HTMLScriptElement | null
const d = script?.dataset
if (d?.endpoint) {
  mount({
    endpoint: d.endpoint,
    title: d.title,
    accent: d.accent,
    suggestions: d.suggestions ? d.suggestions.split('|') : undefined,
    placement: d.placement === "bottom-left" ? "bottom-left" : "bottom-right",
    showOnly: d.showOnly === 'true',
    offset: { side: d.offsetSide ? Number(d.offsetSide) : undefined, bottom: d.offsetBottom ? Number(d.offsetBottom) : undefined },
  })
}
