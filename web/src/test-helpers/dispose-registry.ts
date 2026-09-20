// Imports no production module: `engine-fixture-setup.ts` loads this before every
// test file, and a production module evaluated that early is the one a later
// `vi.mock` in the test file can no longer replace.
export interface Disposable {
  dispose(): void;
}

const live = new Set<Disposable>();

/** Registers an engine, renderer, scroll controller or any other instance a test built. */
export function registerForDispose<T extends Disposable>(instance: T): T {
  live.add(instance);
  return instance;
}

export function unregisterForDispose(instance: Disposable): void {
  live.delete(instance);
}

/** Disposes every registered instance, newest first. */
export function disposeAllFixtures(): void {
  const pending = [...live].reverse();
  live.clear();
  for (const instance of pending) {
    instance.dispose();
  }
}
