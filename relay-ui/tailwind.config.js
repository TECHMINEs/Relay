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
    // Sharp Edged Plasma: strictly sharp corners everywhere
    borderRadius: {
      none: "0px", sm: "0px", DEFAULT: "0px", md: "0px", lg: "0px",
      xl: "0px", "2xl": "0px", "3xl": "0px", full: "0px",
    },
    extend: {
      colors: {
        background: "#000000",
        foreground: "#ededed",
        red: { 400: "#FF3B3B", 500: "#FF0000", 600: "#FF0000", 700: "#DC2626" },
        orange: { 400: "#FF7A45", 500: "#FF5F1F" },
        cyan: { 300: "#66FFFF", 400: "#00FFFF", 500: "#00E5E5" },
        green: { 400: "#39FF14", 500: "#39FF14" },
        emerald: { 400: "#39FF14", 500: "#32E012" },
        cyber: { red: "#FF0000", orange: "#FF5F1F", cyan: "#00FFFF", green: "#39FF14" },
      },
      fontFamily: {
        mono: [
          "ui-monospace",
          "SFMono-Regular",
          "Menlo",
          "Monaco",
          "Consolas",
          "Liberation Mono",
          "Courier New",
          "monospace",
        ],
      },
    },
  },
  plugins: [],
};
