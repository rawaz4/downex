/// <reference types="svelte" />
/// <reference types="vite/client" />

interface ImportMetaEnv {
  /**
   * "wails" | "mock" | "auto". Development defaults to "auto"; a production build always
   * uses the Wails bindings unless built with "mock" (a demo build). See lib/backend.ts.
   */
  readonly VITE_DOWNEX_API?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
