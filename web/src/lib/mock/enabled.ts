// Mock switch. A public variable injected at build time (only the NEXT_PUBLIC_ prefix is readable in the browser).
// Setting NEXT_PUBLIC_MOCK=1 on Vercel makes the whole site run on mock data, no backend needed.
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";
