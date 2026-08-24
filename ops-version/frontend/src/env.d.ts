/// <reference types="vite/client" />

/** 由 vite.config.ts 的 define 注入，来源是构建时的 --build-arg VERSION。 */
declare const __APP_VERSION__: string
declare const __APP_COMMIT__: string
