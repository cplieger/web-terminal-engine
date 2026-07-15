// Vitest config for Stryker mutation runs ONLY (stryker.config.json points
// here; plain `npx vitest run` keeps using vitest.config.ts).
//
// Why a separate config: the cross-language conformance suites
// (render-behavior, render-e2e-golden, wire-golden) read golden fixtures from
// ../../{render,wire}-golden/ — Go-engine output that lives OUTSIDE this npm
// package at the repo root. Stryker sandboxes the package into .stryker-tmp/
// and cannot see files above it, so those suites fail with ENOENT before any
// mutant runs. They still execute in regular CI (ts-ci vitest) and locally;
// mutation testing simply scores against the package-local suites.
import { defineConfig, mergeConfig } from "vitest/config";
import base from "./vitest.config.js";

export default mergeConfig(
  base,
  defineConfig({
    test: {
      exclude: [
        "node_modules/**",
        "src/render-behavior.test.ts",
        "src/render-e2e-golden.test.ts",
        "src/wire-golden.test.ts",
      ],
      // The base config runs with isolate: false for speed; render.ts holds
      // module-scope state (its LineStore) that some suites deliberately swap
      // via the store-swap API. Stryker groups/filters test files differently
      // than a plain vitest run, so without per-file isolation a stub store
      // installed by one file leaks into the next ("store.applyScreen is not
      // a function"). Mutation runs are weekly — pay the isolation cost.
      isolate: true,
    },
  }),
);
