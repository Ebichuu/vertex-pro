import '@testing-library/jest-dom/vitest'
import { afterAll, afterEach } from 'vitest'
import { server } from '../mocks/server'

server.listen({ onUnhandledRequest: 'error' })
afterEach(() => server.resetHandlers())
afterAll(() => server.close())

Object.defineProperty(window, 'scrollTo', {
  configurable: true,
  value: () => undefined,
})
