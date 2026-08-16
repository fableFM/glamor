/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Базовый URL REST/WS демона glamord (default http://127.0.0.1:7380). */
  readonly VITE_GLAMOR_API?: string
  /** Токен localhost-API (D-08); в браузере можно переопределить через localStorage. */
  readonly VITE_GLAMOR_TOKEN?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
