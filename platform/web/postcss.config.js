// CommonJS to match tailwind.config.js. package.json declares no "type", so
// Node treats .js as CommonJS; an ESM export here only worked via Node's
// reparse fallback, which it warns about on every build.
module.exports = {
  plugins: {
    tailwindcss: {},
    autoprefixer: {},
  },
};
