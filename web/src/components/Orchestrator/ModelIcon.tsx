import { useId } from 'react'

// Whether a model of the picker is answered by Gemini (the local Google AI Mode one) or by Claude.
export const isGeminiModel = (model: string) => model.startsWith('local:')

// The mark of the model in use, in its own color: Claude's orange burst, or Gemini's
// blue-to-pink four-pointed star (the local Google AI Mode model).
export function ModelIcon({ model, size = 16, className }: { model: string; size?: number; className?: string }) {
  const gradient = useId()
  if (isGeminiModel(model)) {
    return (
      <svg width={size} height={size} viewBox="0 0 24 24" className={className} role="img" aria-label="Gemini">
        <defs>
          <linearGradient id={gradient} x1="3" y1="21" x2="21" y2="3" gradientUnits="userSpaceOnUse">
            <stop offset="0" stopColor="#4285F4" />
            <stop offset="0.5" stopColor="#9B72CB" />
            <stop offset="1" stopColor="#D96570" />
          </linearGradient>
        </defs>
        <path
          fill={`url(#${gradient})`}
          d="M12 1.5c.6 5.6 4.9 9.9 10.5 10.5-5.6.6-9.9 4.9-10.5 10.5C11.4 16.9 7.1 12.6 1.5 12 7.1 11.4 11.4 7.1 12 1.5z"
        />
      </svg>
    )
  }
  const rays = Array.from({ length: 10 }, (_, i) => i * 36)
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" className={className} role="img" aria-label="Claude">
      <g stroke="#D97757" strokeWidth="2.3" strokeLinecap="round">
        {rays.map((deg, i) => (
          <line key={deg} x1="12" y1={i % 2 ? 6.5 : 4.5} x2="12" y2="10" transform={`rotate(${deg} 12 12)`} />
        ))}
      </g>
    </svg>
  )
}
