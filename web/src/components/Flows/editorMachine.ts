import { assign, fromPromise, setup } from 'xstate'
import type { FlowProblem } from '../../types'

// The editor's life: a draft saves itself shortly after a change, publishing saves
// first, and looking at a past run locks the canvas. The actors are provided by the view;
// they read the canvas when invoked, so a save always holds what was on screen then.

export interface EditorContext {
  problems: FlowProblem[]
  error: string
  changedAgain: boolean // edited while a save was in flight: that save does not hold it
  publishAfterSave: boolean
  unpublished: boolean // the draft has changes the published version lacks
}

export type EditorEvent =
  | { type: 'OPEN' }
  | { type: 'CLOSE' }
  | { type: 'CHANGE' }
  | { type: 'SAVE' }
  | { type: 'PUBLISH' }
  | { type: 'VIEW_RUN' }
  | { type: 'BACK' }

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

export const editorMachine = setup({
  types: { context: {} as EditorContext, events: {} as EditorEvent },
  actors: {
    save: fromPromise<void>(async () => {}),
    publish: fromPromise<FlowProblem[]>(async () => []),
  },
  delays: { autosave: 1000 },
  actions: {
    edited: assign({ unpublished: true, error: '' }),
  },
  guards: {
    changedAgain: ({ context }) => context.changedAgain,
    publishAfterSave: ({ context }) => context.publishAfterSave,
    changedAgainAndPublish: ({ context }) => context.changedAgain && context.publishAfterSave,
  },
}).createMachine({
  id: 'flowEditor',
  initial: 'empty',
  context: { problems: [], error: '', changedAgain: false, publishAfterSave: false, unpublished: false },
  on: {
    OPEN: {
      target: '.clean',
      actions: assign({ problems: [], error: '', changedAgain: false, publishAfterSave: false, unpublished: false }),
    },
    CLOSE: '.empty',
  },
  states: {
    empty: {},
    clean: {
      on: {
        CHANGE: { target: 'dirty', actions: 'edited' },
        PUBLISH: 'publishing',
        VIEW_RUN: 'viewingRun',
      },
    },
    dirty: {
      after: { autosave: 'saving' },
      on: {
        CHANGE: { target: 'dirty', reenter: true, actions: 'edited' },
        SAVE: 'saving',
        PUBLISH: { target: 'saving', actions: assign({ publishAfterSave: true }) },
      },
    },
    saving: {
      entry: assign({ changedAgain: false, error: '' }),
      invoke: {
        src: 'save',
        onDone: [
          { guard: 'changedAgainAndPublish', target: 'saving', reenter: true },
          { guard: 'changedAgain', target: 'dirty' },
          { guard: 'publishAfterSave', target: 'publishing' },
          { target: 'clean' },
        ],
        // no retry on its own: it stays visibly unsaved until the next edit or click
        onError: {
          target: 'saveFailed',
          actions: assign({ publishAfterSave: false, error: ({ event }) => message(event.error) }),
        },
      },
      on: {
        CHANGE: { actions: [assign({ changedAgain: true }), 'edited'] },
        PUBLISH: { actions: assign({ publishAfterSave: true }) },
      },
    },
    saveFailed: {
      on: {
        CHANGE: { target: 'dirty', actions: 'edited' },
        SAVE: 'saving',
        PUBLISH: { target: 'saving', actions: assign({ publishAfterSave: true }) },
      },
    },
    publishing: {
      entry: assign({ publishAfterSave: false, changedAgain: false, error: '' }),
      invoke: {
        src: 'publish',
        onDone: [
          {
            guard: 'changedAgain',
            target: 'dirty',
            actions: assign({ problems: ({ event }) => event.output }),
          },
          {
            target: 'clean',
            actions: assign({
              problems: ({ event }) => event.output,
              unpublished: ({ event, context }) => (event.output.length > 0 ? context.unpublished : false),
            }),
          },
        ],
        onError: { target: 'clean', actions: assign({ error: ({ event }) => message(event.error) }) },
      },
      on: { CHANGE: { actions: [assign({ changedAgain: true }), 'edited'] } },
    },
    viewingRun: {
      on: { BACK: 'clean', VIEW_RUN: {} },
    },
  },
})
