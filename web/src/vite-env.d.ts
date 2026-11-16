/// <reference types="vite/client" />

// Prism's grammar modules extend the global Prism when they load and export
// nothing; viewer/grammars.ts imports them only for that.
declare module 'prismjs/components/*'
