/*
 * ESLint 8 configuration for the portal.
 *
 * ESLint 8 reads .eslintrc.cjs; ESLint 9 reads eslint.config.js and ignores
 * this file entirely. If the pinned eslint devDependency is ever moved to 9,
 * this file has to be rewritten as flat config rather than merely edited, and
 * the plugin versions below move with it.
 *
 * Type-aware rules are on. They cost a TypeScript program build per run, which
 * is what makes them able to see across files: no-floating-promises and
 * no-misused-promises are exactly the class of bug an untyped lint cannot find,
 * and both are easy to write in a React codebase that awaits fetch wrappers.
 */
module.exports = {
  root: true,
  env: {
    browser: true,
    es2020: true,
  },
  parser: '@typescript-eslint/parser',
  parserOptions: {
    ecmaVersion: 'latest',
    sourceType: 'module',
    project: ['./tsconfig.json'],
    tsconfigRootDir: __dirname,
  },
  plugins: ['@typescript-eslint', 'react-hooks', 'react-refresh'],
  extends: [
    'eslint:recommended',
    'plugin:@typescript-eslint/recommended',
    'plugin:@typescript-eslint/recommended-requiring-type-checking',
    'plugin:react-hooks/recommended',
  ],
  ignorePatterns: ['dist', 'node_modules', '.eslintrc.cjs'],
  rules: {
    // Vite's fast refresh only works when a module exports components alone.
    // A warning rather than an error: it is a development-experience rule, and
    // --max-warnings 0 makes it fail the build anyway.
    'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],

    // An unused argument named _ is deliberate; an unused local is not.
    '@typescript-eslint/no-unused-vars': [
      'error',
      { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
    ],
  },
  // No per-directory relaxations. Test files were tried with the type-aware
  // rules switched off for them and passed either way, so the exemption was
  // removed: a relaxation that is not doing work is indistinguishable from one
  // that is hiding something.
};
