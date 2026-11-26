import { rmSync } from 'node:fs'
import path from 'node:path'

// Vite shares internal/server/web with ttyd's standalone enhancement scripts.
// Remove only generated hashed assets; never empty the containing directory.
rmSync(path.resolve(import.meta.dirname, '../../internal/server/web/assets'), { recursive: true, force: true })
