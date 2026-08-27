import eslint from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";

export default tseslint.config(
  {ignores: ["dist", "coverage", "playwright-report", "src/api/schema.gen.ts"]},
  eslint.configs.recommended,
  ...tseslint.configs.strictTypeChecked,
  ...tseslint.configs.stylisticTypeChecked,
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: {
      parserOptions: {projectService: true, tsconfigRootDir: import.meta.dirname},
      globals: {...globals.browser, ...globals.node},
    },
    rules: {
      "@typescript-eslint/consistent-type-definitions": ["error", "type"],
      "@typescript-eslint/no-misused-promises": ["error", {checksVoidReturn: {attributes: false}}],
    },
  },
  {
    files: ["**/*.test.{ts,tsx}", "e2e/**/*.ts"],
    languageOptions: {globals: globals.node},
  },
);
