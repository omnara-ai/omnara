import '@/polyfills'
import '@/styles/index.css'
import '@/fonts'

import { OmnaraClientProvider } from '@omnara/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { queryClient } from '@/lib/query'
import { router } from '@/router'
import { omnaraClient } from '@/transport'

const rootElement = document.getElementById('root')
if (!rootElement) {
  throw new Error('root element not found')
}

createRoot(rootElement).render(
  <StrictMode>
    <OmnaraClientProvider client={omnaraClient}>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} context={{ queryClient, omnaraClient }} />
      </QueryClientProvider>
    </OmnaraClientProvider>
  </StrictMode>,
)
