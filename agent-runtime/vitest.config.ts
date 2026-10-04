import { configDefaults, defineConfig } from "vitest/config";

export default defineConfig({
  test: { exclude: [...configDefaults.exclude, "dist/**"], maxWorkers: 2, testTimeout: 15000 },
});
