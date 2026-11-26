import { createRoot } from 'react-dom/client'
import { WorkspacePage } from './components/WorkspacePage'
import './index.css'

if (window.top !== window.self) {
  document.documentElement.textContent = 'The terminal could not load. Close this tab and open it again.'
  throw new Error('workspace must not run inside an iframe')
}

createRoot(document.getElementById('root')!).render(<WorkspacePage />)
