import { create } from 'zustand'
import type { ConnectionStatus } from '../lib/events'

interface ConnectionState {
  /** online — WS жив (synced получен); reconnecting — обрыв, идёт reconnect; offline — клиент остановлен */
  status: ConnectionStatus
  setStatus: (status: ConnectionStatus) => void
}

export const useConnectionStore = create<ConnectionState>((set) => ({
  status: 'offline',
  setStatus: (status) => set({ status }),
}))
