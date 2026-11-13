/** @type {import('tailwindcss').Config} */
module.exports = {
  // Resolve globs from this file so styles compile no matter which directory `next` is launched from
  content: {
    relative: true,
    files: [
      "./src/pages/**/*.{js,ts,jsx,tsx,mdx}",
      "./src/components/**/*.{js,ts,jsx,tsx,mdx}",
      "./src/app/**/*.{js,ts,jsx,tsx,mdx}",
    ],
  },
  theme: {
    // Sharp corners everywhere, as on techmines.github.io
    borderRadius: {
      none: "0px", sm: "0px", DEFAULT: "0px", md: "0px", lg: "0px",
      xl: "0px", "2xl": "0px", "3xl": "0px", full: "0px",
    },
    extend: {
      colors: {
        // TECHMINES portfolio tokens (techmines.github.io)
        panel: { DEFAULT: '#0E0E0E', 2: '#171717' },
        line: '#2E2E2E',
        stroke: '#4D4D4D',
        dim: '#A9A9A9',
        body: '#E2E2E2',
        background: "#000000",
        foreground: "#ededed",
        red: { 400: "#FF3B3B", 500: "#FF2626", 600: "#FF2626", 700: "#F5141F" },
        green: { 400: "#39FF14", 500: "#39FF14" },
      },
      fontFamily: {
        display: ['var(--font-chakra)', '"Chakra Petch"', '"Arial Narrow"', 'sans-serif'],
        sans: ['var(--font-plex)', '"IBM Plex Sans"', 'system-ui', 'sans-serif'],
        mono: ['var(--font-jetbrains)', '"JetBrains Mono"', 'ui-monospace', 'Menlo', 'monospace'],
      },
    },
  },
  plugins: [],
};
