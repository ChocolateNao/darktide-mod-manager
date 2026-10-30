import js from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";
import importPlugin from "eslint-plugin-import";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import simpleImportSort from "eslint-plugin-simple-import-sort";
import stylistic from "@stylistic/eslint-plugin";
import eslintPluginPrettierRecommended from "eslint-plugin-prettier/recommended";
import eslintConfigPrettier from "eslint-config-prettier";

import eslintPluginTailwindcss from "eslint-plugin-tailwindcss";

export default [
  { ignores: ["dist", "wailsjs", "**/*.config.js", "coverage"] },
  {
    languageOptions: {
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  eslintPluginTailwindcss.configs["flat/recommended"] ||
    eslintPluginTailwindcss.configs.recommended,
  stylistic.configs.customize({
    indent: 2,
    quotes: "double",
    semi: true,
    jsx: true,
  }),
  {
    files: ["**/*.{js,jsx}", "**/*.{ts,tsx}"],
    languageOptions: { globals: globals.browser },
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
      "simple-import-sort": simpleImportSort,
      import: importPlugin,
      "@stylistic": stylistic,
    },
    rules: {
      ...reactHooks.configs.flat.recommended.rules,
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true, allowExportNames: ["buttonVariants"] },
      ],
      "@typescript-eslint/consistent-type-imports": [
        "error",
        {
          prefer: "type-imports", // Options: 'type-imports' or 'no-type-imports'
          fixStyle: "separate-type-imports", // Options: 'separate-type-imports' or 'inline-type-imports'
        },
      ],
      // (Optional) Enforces 'export type' for type-only exports
      "@typescript-eslint/consistent-type-exports": "error",
      // Prop interfaces are intentionally empty when they only extend a DOM or
      // Radix props type; the name is what call sites read.
      "@typescript-eslint/no-empty-object-type": [
        "error",
        { allowInterfaces: "always" },
      ],
      "no-console": ["warn", { allow: ["error"] }],
      "simple-import-sort/exports": "warn",
      "import/first": "error",
      "import/newline-after-import": "error",
      "import/no-duplicates": ["error", { considerQueryString: true }],
      "@stylistic/indent-binary-ops": ["error", 2],
      // "@stylistic/max-len": ["error", {
      //   code: 80,
      // }],
      "@stylistic/comma-dangle": [
        "error",
        {
          arrays: "always-multiline",
          objects: "always-multiline",
          imports: "always-multiline",
          exports: "always-multiline",
          functions: "never",
        },
      ],
      "@stylistic/key-spacing": [
        "error",
        {
          multiLine: {
            beforeColon: false,
            afterColon: true,
          },
          align: {
            beforeColon: false,
            afterColon: true,
            on: "value",
          },
        },
      ],
      "@stylistic/no-extra-semi": "error",
      "@stylistic/no-multi-spaces": "off",
      "import/no-extraneous-dependencies": ["error", { devDependencies: true }],
      "simple-import-sort/imports": [
        "error",
        {
          groups: [
            // Core packages come first.
            ["^react", "^@?\\w", "^eslint", "^wailsjs"],
            // Internal packages (ALIASES ARE HERE).
            [
              "^(components|ui|context|api|pages|assets|resources|shared|models|styles|stores|layouts|utils|bridge|hooks|lib|types|views)(/.*|$)",
            ],
            // Side effect imports.
            ["^\\u0000"],
            // Parent imports. Put `..` last.
            ["^\\.\\.(?!/?$)", "^\\.\\./?$"],
            // Other relative imports. Put same-folder imports and `.` last.
            ["^\\./(?=.*/)(?!/?$)", "^\\.(?!/?$)", "^\\./?$"],
            // Style imports.
            ["^.+\\.?(css|scss)$"],
          ],
        },
      ],
    },
  },
  eslintConfigPrettier,
  eslintPluginPrettierRecommended,

];
