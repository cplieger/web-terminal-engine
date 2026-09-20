import { afterEach } from "vitest";

import { disposeAllFixtures } from "./test-helpers/dispose-registry.js";

afterEach(() => {
  disposeAllFixtures();
});
