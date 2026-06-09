import { useEffect, useState } from 'react'

function getWailsApp() {
  return (window as any).go?.main?.App as {
    Minimise: () => void
    ToggleMaximise: () => void
    IsMaximised: () => Promise<boolean>
    CloseApp: () => void
  } | undefined
}

export function TitleBar() {
  const [isMaximised, setIsMaximised] = useState(false)
  const [isWails, setIsWails] = useState(false)

  useEffect(() => {
    const app = getWailsApp()
    if (!app) return
    setIsWails(true)
    app.IsMaximised().then(setIsMaximised).catch(() => {})
  }, [])

  if (!isWails) return null

  const handleMinimise = () => getWailsApp()?.Minimise()

  const handleToggleMax = () => {
    getWailsApp()?.ToggleMaximise()
    setIsMaximised(v => !v)
  }

  const handleClose = () => getWailsApp()?.CloseApp()

  return (
    <div className="titlebar">
      <div className="titlebar__logo">
        <span className="titlebar__lu">Lu</span>
      </div>

      {/* drag region */}
      <div className="titlebar__drag" style={{ ['--wails-draggable' as string]: 'drag' }} />

      <div className="titlebar__controls">
        <button className="titlebar__btn" onClick={handleMinimise} title="Minimize">
          <svg width="11" height="1" viewBox="0 0 11 1" fill="none">
            <path d="M0 0.5H11" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/>
          </svg>
        </button>

        <button className="titlebar__btn" onClick={handleToggleMax} title={isMaximised ? 'Restore' : 'Maximize'}>
          {isMaximised ? (
            <svg width="11" height="11" viewBox="0 0 11 11" fill="none">
              <rect x="3" y="0.5" width="7.5" height="7.5" rx="1" stroke="currentColor" strokeWidth="1.3"/>
              <path d="M1 3.5V9.5C1 10.05 1.45 10.5 2 10.5H8" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round"/>
            </svg>
          ) : (
            <svg width="11" height="11" viewBox="0 0 11 11" fill="none">
              <rect x="0.5" y="0.5" width="10" height="10" rx="1.5" stroke="currentColor" strokeWidth="1.3"/>
            </svg>
          )}
        </button>

        <button className="titlebar__btn titlebar__btn--close" onClick={handleClose} title="Close">
          <svg width="11" height="11" viewBox="0 0 11 11" fill="none">
            <path d="M1 1L10 10M10 1L1 10" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"/>
          </svg>
        </button>
      </div>
    </div>
  )
}
