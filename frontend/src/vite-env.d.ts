/// <reference types="vite/client" />

interface ImportMetaEnv {
  // "true" serves demo data instead of calling a backend (npm run demo).
  readonly VITE_DEMO?: string;
}
