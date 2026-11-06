// Lays a commit list out as the vertical branch graph. Rows come newest first, as git log gives
// them. Each row says where its node sits and which lines cross its top half (down to the node's
// height) and its bottom half. Pure, so the tests can walk the shapes without a browser.

export interface GraphSegment {
  from: number // lane at the upper end
  to: number   // lane at the lower end
  color: number
}

export interface GraphRow {
  lane: number
  color: number
  merge: boolean
  top: GraphSegment[]
  bottom: GraphSegment[]
  // the lanes alive under this row, drawn straight through anything shown below the commit
  through: { lane: number; color: number }[]
}

export interface GraphLayout {
  rows: GraphRow[]
  lanes: number // how wide the widest row is
}

interface Stream { sha: string; color: number }

// The main branch keeps lane 0 and color 0: the one straight line the eye can rest on.
const TRUNK_COLOR = 0

export function layoutGraph(commits: { sha: string; parents: string[] }[], trunkTip?: string): GraphLayout {
  const bySha = new Map(commits.map(c => [c.sha, c]))
  const trunk = new Set<string>()
  for (let s = trunkTip ? bySha.get(trunkTip) : undefined; s && !trunk.has(s.sha); s = bySha.get(s.parents[0])) {
    trunk.add(s.sha)
    // the parent of the last loaded commit is not in the list, but its line still belongs on lane 0
    if (s.parents[0] && !bySha.has(s.parents[0])) trunk.add(s.parents[0])
  }
  const firstFree = trunk.size > 0 ? 1 : 0

  let lanes: (Stream | null)[] = []
  let nextColor = 1
  let widest = 0
  const freeLane = () => {
    for (let i = firstFree; i < lanes.length; i++) if (!lanes[i]) return i
    return Math.max(lanes.length, firstFree)
  }
  const place = (i: number, s: Stream) => {
    while (lanes.length <= i) lanes.push(null)
    lanes[i] = s
  }

  const rows: GraphRow[] = commits.map(c => {
    const waiting = lanes.flatMap((s, i) => (s?.sha === c.sha ? [i] : []))
    const onTrunk = trunk.has(c.sha)
    const lane = onTrunk ? 0 : waiting[0] ?? freeLane()
    const color = onTrunk ? TRUNK_COLOR : waiting.length ? lanes[waiting[0]]!.color : nextColor++

    const top: GraphSegment[] = []
    lanes.forEach((s, i) => {
      if (s) top.push({ from: i, to: s.sha === c.sha ? lane : i, color: s.color })
    })
    for (const i of waiting) lanes[i] = null
    if (lane < lanes.length) lanes[lane] = null

    const bottom: GraphSegment[] = []
    lanes.forEach((s, i) => { if (s) bottom.push({ from: i, to: i, color: s.color }) })

    c.parents.forEach((parent, p) => {
      // A line that already waits for this parent has no commit before it, so joining it says the
      // truth. The first parent joins only toward the left: the line it is on stays straight, and
      // both arrive at the parent on their own.
      const existing = lanes.findIndex(s => s?.sha === parent)
      if (existing >= 0 && (p > 0 || existing < lane)) {
        bottom.push({ from: lane, to: existing, color: p === 0 ? color : lanes[existing]!.color })
      } else if (p === 0 && !lanes[lane]) {
        place(lane, { sha: parent, color })
        bottom.push({ from: lane, to: lane, color })
      } else {
        const target = freeLane()
        const streamColor = nextColor++
        place(target, { sha: parent, color: streamColor })
        bottom.push({ from: lane, to: target, color: streamColor })
      }
    })

    while (lanes.length && !lanes[lanes.length - 1]) lanes = lanes.slice(0, -1)
    widest = Math.max(widest, lane + 1, lanes.length, ...top.map(s => s.from + 1))
    return {
      lane,
      color,
      merge: c.parents.length > 1,
      top,
      bottom,
      through: lanes.flatMap((s, i) => (s ? [{ lane: i, color: s.color }] : [])),
    }
  })
  return { rows, lanes: widest }
}
