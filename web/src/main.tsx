import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import './i18n'
import { queryClient } from './app/query-client'
import { router } from './app/router'
import { ErrorBoundary } from './components/ui/ErrorBoundary'
import { AuthGate } from './components/auth/AuthGate'
import './styles.css'

export function App() {
  return (
    <ErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <AuthGate><RouterProvider router={router} /></AuthGate>
      </QueryClientProvider>
    </ErrorBoundary>
  )
}


const rootElement = document.getElementById('root')
if (rootElement) {
  createRoot(rootElement).render(<StrictMode><App /></StrictMode>)
}


