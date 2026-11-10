const path = require("path");

module.exports = {
  plugins: {
    // Point at this project's config explicitly so it is found regardless of the launch directory
    tailwindcss: { config: path.join(__dirname, "tailwind.config.js") },
    autoprefixer: {},
  },
};
