import '@testing-library/jest-dom/vitest'

Object.defineProperty(window, 'matchMedia', {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => undefined,
    removeListener: () => undefined,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false,
  }),
})

class TestResizeObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
}

Object.defineProperty(window, 'focus', { writable: true, value: () => undefined })
Object.defineProperty(window, 'ResizeObserver', { writable: true, value: TestResizeObserver })
Object.defineProperty(globalThis, 'ResizeObserver', { writable: true, value: TestResizeObserver })
Object.defineProperty(Element.prototype, 'scrollIntoView', { writable: true, value: () => undefined })
const stubFrameWindow = {
  focus: () => undefined,
  postMessage: () => undefined,
}
Object.defineProperty(HTMLIFrameElement.prototype, 'contentWindow', { configurable: true, get: () => stubFrameWindow })
Object.defineProperty(HTMLIFrameElement.prototype, 'contentDocument', { configurable: true, get: () => null })
Object.defineProperty(URL, 'createObjectURL', { writable: true, value: () => 'blob:test' })
Object.defineProperty(URL, 'revokeObjectURL', { writable: true, value: () => undefined })
