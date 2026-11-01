import fc from "fast-check";

fc.configureGlobal({
  numRuns: 1000,
  verbose: fc.VerbosityLevel.VeryVerbose,
  endOnFailure: false,
});

// Outermost plugin, so no per-call limit outlasts 10 s. Only its own interrupt fails:
// a per-call interruptAfterTimeLimit passes silently unless it sets failOnInterrupt too.
fc.installGlobalPlugin(fc.interruptAfterTimeLimit(10000, { failOnInterrupt: true }));
