import Dexie, { type Table } from 'dexie'

interface ReadMessage {
  id: string
}

class TTSDatabase extends Dexie {
  readMessages!: Table<ReadMessage, string>

  constructor() {
    super('tts_read_messages')
    this.version(1).stores({
      readMessages: 'id',
    })
  }
}

export const ttsDb = new TTSDatabase()
