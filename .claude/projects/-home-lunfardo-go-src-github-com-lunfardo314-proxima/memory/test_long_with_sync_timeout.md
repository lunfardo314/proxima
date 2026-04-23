---
name: test_long_with_sync_timeout
description: TestAttachConflicts1Attacher/long_with_sync hangs (600s timeout) - likely caused by recent rate control changes
type: project
---

`TestAttachConflicts1Attacher/long_with_sync` in `tests/` package consistently times out (600s).

**Why:** Likely related to rate control changes from recent sessions (attacher cap, seq_attach/nonseq_attach gates, depth cap). The test was working before these changes were introduced on develop07.

**How to apply:** Investigate in a dedicated session. Check if the test relies on non-pulled non-seq transactions passing through attach queues, or if the attacher cap / depth cap blocks solidification paths needed by the test. The `long_with_sync` subtest name suggests it involves forward-sync which interacts with the depth cap exemption code.
