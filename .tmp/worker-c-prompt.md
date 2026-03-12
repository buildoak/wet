## TASK: Add tests for CompressSelected in pipeline_test.go

You are adding tests for the CompressSelected function that was just added to the wet Go proxy pipeline.

### Files to read first:
- pipeline/selective.go (the function you are testing - CompressSelected)
- pipeline/pipeline_test.go (existing tests - add your tests here)
- pipeline/pipeline.go (understand CompressRequest for reference)
- pipeline/bypass.go (understand ShouldBypass)
- pipeline/tombstone.go (understand IsTombstone, CreateTombstone)
- messages/staleness.go (understand ToolResultInfo)

### What to add to pipeline/pipeline_test.go:

Add these test functions at the end of the file (after the existing helper functions):

1. **TestCompressSelectedTargetsOnlySpecifiedIDs** — Create a request with tool results t1 (git status, large) and t2 (npm install, large). Call CompressSelected with only ["t1"]. Verify t1 is compressed (IsTombstone) and t2 is NOT compressed. Verify result.Compressed == 1.

2. **TestCompressSelectedEmptyIDs** — Call CompressSelected with nil IDs and empty IDs. Verify result.Compressed == 0 in both cases.

3. **TestCompressSelectedRespectsErrors** — Create a request with an error tool result. Call CompressSelected targeting its ID with cfg.Bypass.PreserveErrors = true. Verify it's not compressed (SkippedBypass >= 1).

4. **TestCompressSelectedSkipsTombstones** — Create a request where the tool result content is already a tombstone string. Call CompressSelected targeting its ID. Verify it's not compressed.

Use the existing helper functions: rawJSON, makeGitStatusOutput, makeNpmInstallOutput, mustToolResultContent.

Set cfg.Staleness.Threshold = 1 (or 0 where needed to ensure staleness) and cfg.Bypass.ContentPatterns = nil to avoid pattern-based bypasses interfering with tests.

Make sure each test has enough assistant turns to make the target results stale per the threshold.

### IMPORTANT:
- Do NOT modify any .go files other than pipeline/pipeline_test.go
- After making changes, run: go test ./pipeline/...
- If tests fail, fix them. All tests MUST pass.
- Return a 3-5 sentence summary of test results.
