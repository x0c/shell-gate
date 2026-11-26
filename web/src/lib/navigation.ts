export function assignLocation(path: string) {
  window.location.assign(path)
}

export function replaceLocation(path: string) {
  window.location.replace(path)
}

export function replaceHistory(path: string) {
  window.history.replaceState(null, '', path)
}
