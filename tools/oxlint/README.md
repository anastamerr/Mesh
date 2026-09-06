# Vendored anti-slop rules

Source: https://github.com/dmmulroy/anti-slop

Revision: `e8c4880471b23ab7f216fba7b27d173a6ef07d4c` (MIT; license included). Copied using the upstream installation skill.

Mesh enables all generic rules in `.oxlintrc.json`. The optional Effect rules are not enabled because Mesh does not use Effect. Update this source deliberately, review the diff, and keep `oxlint` and `@oxlint/plugins` pinned to the same version. Run `npm run lint` after updates.

Oxlint and `@oxlint/plugins` are pinned to `1.81.0`. Use Node.js 22.18+ so the TypeScript plugin entry point can load without an extra transpilation tool. `tools/oxlint/package.json` marks the vendored tooling as ESM without changing the controller’s CommonJS build.
