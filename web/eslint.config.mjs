// The shared ruleset is ./eslint.config.base.mjs, synced from cplieger/ci;
// this file adds the end-to-end harness's deltas.
import baseConfig from "./eslint.config.base.mjs";

// The harness sits outside every tsconfig. Set on the base's own entry: a later
// entry naming projectService left every tsconfig file "not found by the project
// service" (typescript-eslint 8).
const withHarness = (config) => {
  const projectService = config.languageOptions?.parserOptions?.projectService;
  if (!projectService) {
    return config;
  }
  const allowDefaultProject = [...projectService.allowDefaultProject, "e2e/e2e-harness.ts"];
  return {
    ...config,
    languageOptions: {
      ...config.languageOptions,
      parserOptions: {
        ...config.languageOptions.parserOptions,
        projectService: { ...projectService, allowDefaultProject },
      },
    },
  };
};

export default [
  ...baseConfig.map(withHarness),
  { ignores: ["e2e/render-bench.mjs"] },
  {
    files: ["e2e/**/*.ts"],
    rules: {
      "@typescript-eslint/consistent-type-imports": [
        "error",
        { prefer: "type-imports", fixStyle: "inline-type-imports", disallowTypeAnnotations: false },
      ],
    },
  },
];
