import type { MouseEvent } from 'react'

// Spread on a modal's backdrop: `<div className="overlay" {...backdropClose(onClose)}>`.
// It closes only when the press AND the release both happen on the backdrop itself.
// A plain onClick also fires when text is selected inside the modal and the mouse is
// released outside it (the click lands on the common ancestor, the backdrop).
let pressedOnBackdrop = false

export function backdropClose(onClose: () => void) {
  return {
    onMouseDown: (e: MouseEvent) => { pressedOnBackdrop = e.target === e.currentTarget },
    onClick: (e: MouseEvent) => {
      const close = pressedOnBackdrop && e.target === e.currentTarget
      pressedOnBackdrop = false
      if (close) onClose()
    },
  }
}
