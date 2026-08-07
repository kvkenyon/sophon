#!/usr/bin/env bash
set -euo pipefail

if [[ ${SOPHON_RUN_CRASH_INJECTION:-} != 1 ]]; then
  echo "crash injection is opt-in; rerun with SOPHON_RUN_CRASH_INJECTION=1" >&2
  exit 2
fi

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$repo_root"

go test -tags=crash_injection ./internal/recovery -run '^TestCrashInjectionSuite$' -count=1 -v

# These focused proofs bind the process-boundary matrix to the production
# reconciliation paths without adding the crash tag to ordinary test runs.
go test ./internal/treehouse -run 'TestReconcileAdoptsLeaseAcquiredBeforeDatabaseRecord|TestReleaseReconcilesCrashAfterExternalConditionalReturn' -count=1
go test ./internal/worker -run 'TestCompletionResumerVerifiesExternalStateAndConvergesOnOneCommand' -count=1
go test ./internal/recovery -run 'TestStartupMissingWorkerEscalatesWithoutReplacement|TestStartupObservesExistingWorkerSession|TestStartupResumesStructuredCompletion|TestStartupMarksPartialValidationExplicitlyResumable' -count=1
go test ./internal/delivery -run 'TestStartupReconcileReplaysPersistedDeliveryInputs|TestStartupDoesNotSilentlyRepeatInterruptedNoMistakesGate|TestStartupFinishesReleaseThatCompletedBeforeDatabaseRecord|TestStartupResumesReleaseIntentBeforeExternalReturn' -count=1

if [[ ${SOPHON_CRASH_HERDR_LAB:-} == 1 ]]; then
  HERDR_LAB_HELPER=${HERDR_LAB_HELPER:-/Users/kevin/github/kvkenyon/research/firstmate/bin/fm-herdr-lab.sh}
  HERDR_LAB_SESSION=$("$HERDR_LAB_HELPER" name sophon-m11-recovery)
  export HERDR_LAB_HELPER HERDR_LAB_SESSION
  trap '"$HERDR_LAB_HELPER" teardown "$HERDR_LAB_SESSION"' EXIT
  "$HERDR_LAB_HELPER" provision "$HERDR_LAB_SESSION"
  HERDR_LAB_PROVISIONED=1 SOPHON_RECOVERY_SMOKE=1 \
    go test ./internal/recovery -run '^TestRealHerdrRestartReconciliation$' -count=1 -v
  "$HERDR_LAB_HELPER" teardown "$HERDR_LAB_SESSION"
  trap - EXIT
fi
