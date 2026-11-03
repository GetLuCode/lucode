const PROJECT_COLORS = [
  '#7c8cff', '#4ade80', '#fb923c', '#f87171', '#a78bfa',
  '#38bdf8', '#facc15', '#f472b6', '#34d399', '#60a5fa',
]

export function getProjectColor(name: string): string {
  let hash = 0
  for (let i = 0; i < name.length; i++) hash = name.charCodeAt(i) + ((hash << 5) - hash)
  return PROJECT_COLORS[Math.abs(hash) % PROJECT_COLORS.length]
}

export function getProjectInitials(name: string): string {
  const words = name.trim().split(/[\s\-_.]+/).filter(Boolean)
  if (words.length >= 2) return (words[0][0] + words[1][0]).toUpperCase()
  return name.slice(0, 2).toUpperCase()
}
