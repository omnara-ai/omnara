import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import App from './App.tsx'
import './index.css'
import './integrations/sentry'
import { PostHogProvider } from 'posthog-js/react'

const POSTHOG_KEY = import.meta.env.VITE_POSTHOG_KEY as string | undefined
const POSTHOG_HOST = (import.meta.env.VITE_POSTHOG_HOST as string | undefined) || 'https://us.i.posthog.com'

const withProviders = (node: React.ReactNode) => {
  if (POSTHOG_KEY) {
    return (
      <PostHogProvider apiKey={POSTHOG_KEY} options={{ api_host: POSTHOG_HOST }}>
        {node}
      </PostHogProvider>
    )
  }
  return <>{node}</>
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    {withProviders(<App />)}
  </StrictMode>
);
