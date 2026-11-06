import { createMachine, getNextSnapshot } from 'xstate'

// The lifecycle of a card as XState. The machine comes from the hub (GET /api/card-machine),
// the one place its rules are written; a Go test and a web test check both sides against
// the same fixture. The hub still decides: this only spares a refused move the round trip.

export interface CardFacts {
  hasAgent: boolean
  depsDone: boolean
  blockerAnswered: boolean
}

type MoveEvent = { type: string; facts: CardFacts }

export function buildCardMachine(config: Record<string, unknown>) {
  return createMachine(config as never).provide({
    guards: {
      canStart: ({ event }: { event: MoveEvent }) => event.facts.hasAgent,
      canRestart: ({ event }: { event: MoveEvent }) => event.facts.hasAgent && event.facts.depsDone && event.facts.blockerAnswered,
    },
  } as never)
}

export type CardMachine = ReturnType<typeof buildCardMachine>

export const cardState = (kind: string | undefined) => kind || 'custom'

// Whether a card may move between two states.
export function canMoveCard(machine: CardMachine, from: string, to: string, facts: CardFacts): boolean {
  const state = machine.resolveState({ value: from, context: {} } as never)
  const next = getNextSnapshot(machine as never, state as never, { type: `MOVE_${to.toUpperCase()}`, facts } as never) as { value: unknown }
  return next.value === to
}
