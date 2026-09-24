<!--
Copyright 2026 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Keeping generated scripts from failing on stage

## Goal

Attendees must never see a generated script fail because of what the
sandbox lacks: third-party packages, network access, other languages'
tooling, or time. When a script still fails for any other reason after an
attendee approves it, the agent corrects itself instead of showing a raw
stderr dump, on the phone or on the stage.

At a conference an attendee blames a failing script on the platform, not
the model, so these failures undercut the demo's point.

## Roadmap

This spec is the first of four sub-projects from reviewing the demo as an
attendee. Each gets its own spec, plan and implementation, in this order:

1. **Script failures** (this spec).
2. **Live fleet counts.** The `/stage` header's "2 Physical Worker Pods" and
   "50x Oversubscription" are fixed copy
   (`cmd/conf-gateway/static/stage.html`); read them from the cluster.
3. **Visible resume latency.** Model latency hides how long a resume takes;
   show the resume time per turn separately.
4. **Visible architecture and next steps.** Show on stage that actors have
   no egress and the gateway makes every model call, and give attendees a
   "build your own actor" link.

Out of scope for the whole roadmap: the agent's task being a toy, and the
cost of suspending after every turn (parked until someone runs the numbers).

## Current behavior

- The model is told "standard library only" in two tool parameter
  descriptions (`internal/workload/gemini/tools.go`). `Generate` sets no
  system instruction.
- Before approval, `PythonSyntaxError` compiles the script
  (`internal/workload/gemini/syntax.go`). A script that fails goes back to
  the model once; if the retry fails too, the attendee is asked to approve
  it anyway.
- The model may propose `python`, `bash` or `sh`. Bash cannot be checked
  before it runs.
- Actors have no egress (`substrate-gaps.md`), so any network call fails.
- After approval, `handleApprove` runs the script with a 5s timeout and
  returns the raw result (`internal/workload/server/server.go`). The model
  never sees it. The phone shows `describeRun`'s text; the stage spotlight
  (`stage.js`) and feed cards (`feed.js`, `stage.js`) print stderr in red
  with the exit code.
- The model's view of history is message text only (`BuildContents`); it
  does not see the code it proposed earlier.

## Decisions

| Question | Decision |
|---|---|
| What happens when an approved script fails | The actor asks the model for a fix and shows it as a new approval |
| Where the fix runs | Inside the actor's `/api/approve`, as a relayed turn |
| Languages | Python only |
| Running a script before approval to test it | No. Nothing runs before a human approves it. |
| Fixes per request | One |

## Design

### 1. Before approval

All in `internal/workload/gemini`.

**System instruction.** `Generate` sets `GenerateContentConfig.SystemInstruction`
to text that says scripts run on Python 3.12 with only the standard library,
no network access and a 5 second limit, must print their results, and should
use `statistics`, `csv`, `json`, `collections` and ASCII charts rather than
numpy, pandas or matplotlib. The two `code` parameter descriptions say the
same in one sentence.

**Python only.** Both tool declarations' `language` parameters become an enum
with the single value `python`. A proposal in any other language fails the
check below with "only Python scripts can run in this sandbox".

**The check.** `SyntaxChecker` and `PythonSyntaxError` become `ScriptChecker`
and `ScriptProblem`: `func(ctx, language, code string) string`, returning a
one-line description of the first problem or "". `WithSyntaxChecker` becomes
`WithScriptChecker`. For Python it runs one `python3 -I` pass over the
script on stdin that executes none of it and reports, in order:

1. a syntax error, as today;
2. an import (`import x`, `from x import y`, top-level name of a dotted
   module) that is not in `sys.stdlib_module_names`: "module pandas is not
   installed; use only the standard library";
3. an import of a network module: `socket`, `ssl`, `http`, `urllib`,
   `ftplib`, `smtplib`, `poplib`, `imaplib`, `telnetlib`, `xmlrpc`,
   `socketserver`, `asyncio`: "the sandbox has no network access".

Relative imports (`from . import x`) are a problem too: the script is one
file. Imports built at runtime (`__import__`, `importlib`) are not checked.
If python3 cannot run, the check returns "" as today.

**Retry budget.** A proposal that fails the check goes back to the model once,
as today. If the second proposal fails too, `Generate` does not return it.
It returns a text reply with no approval: "I couldn't write a script that
runs in this sandbox. Try rephrasing the request." `Generate` still runs at
most three model turns.

### 2. After approval

**Failure.** An approved run fails when its exit code is not 0; -1 covers the
timeout and executor errors.

**The fix turn.** When a run fails, the actor is in Gemini mode, and the
pending approval was not itself a fix, `handleApprove`:

1. takes `turnSlot` and starts a `relay.Turn`, as `handleChat` does, and
   answers with `writeStep`, so the gateway relays the turn's model calls as
   it already does for every proxied reply;
2. calls a new `(*gemini.Client).Fix(ctx, history, failed *types.ApprovalRequest,
   run *types.ExecutionResult) (*types.ChatMessage, error)`.

`Fix` sends the history (as `BuildContents` builds it) plus a prompt that
carries the failed code and its stderr, truncated to 2 KiB, and asks for one
sentence on what went wrong and a corrected script through
`execute_sandbox_code`. The corrected script goes through the same check and
retry budget as `Generate`. A reply without an approval, or a failed check
after the retry, is an error.

**The reply.** On success, the turn's reply is one assistant message:

- `Content`: the model's sentence;
- `Execution`: the failed run, with a new field
  `ExecutionResult.Corrected bool` (`json:"corrected,omitempty"`) set;
- `Approval`: the corrected script, recorded by the actor as the pending
  approval and marked as a fix.

The field lives on `ExecutionResult` so the phone, the feed and the
spotlight all see it without new plumbing.

**Budget and errors.** When the failed approval was already a fix, when the
actor is in mock mode, or when `Fix` returns an error, the actor makes no
model call and replies as today but with `Content` set to "That approach
didn't work in this sandbox. Try asking for something simpler." and the run
attached with `Corrected` false. A `Fix` error never falls back to the mock
engine, whose script is unrelated to the request.

**Timing.** The fix turn is bounded by `chatTimeout` (60s), separately from
the run's 5s. It makes at most three model calls, within the gateway's
four per request. The router's route timeout (5m) and parked-request budget
(60s) already cover a chat turn of the same length.

**History.** The failed-run message is not added to the actor's history on
its own; the fix reply is, as one message, like any other assistant reply.

### 3. What attendees see

**Phone (`chat.js`).** The approve handler renders its reply with the same
function as chat replies, so a new approval card appears. A message whose
execution is `Corrected` shows the model's sentence, an amber "Self-corrected"
note with the stderr in a collapsed "What went wrong" `<details>`, and the
new approval card. A failed run that is not corrected shows the plain
message with the stderr in the same collapsed `<details>`. The phone never
shows stderr expanded by default. The approved card's badge stays
"✓ Execution Approved & Executed".

**Gateway.** `onReply` does not store a `Corrected` execution in
`lastResults`, so the stage spotlight keeps showing what it showed before.
It records the corrected run on the action log entry with the new approval,
so the feed can show it.

**Stage spotlight (`stage.js`).** A failed run that is not corrected shows
under "Didn't run" in amber instead of the red exit line, with the first line
of stderr only.

**Feed cards (`feed.js`, `stage.js`).** A `Corrected` run shows an amber
"Self-corrected" chip and the first line of stderr; the card goes on to show
the new proposal. A failed run that is not corrected shows an amber "Didn't
run" chip and the first line of stderr. Successful runs are unchanged.

## Testing

- `internal/workload/gemini`: `ScriptProblem` table tests (syntax error,
  third-party import, dotted import, `from x import y`, relative import,
  network module, clean stdlib script, non-Python language); the system
  instruction is sent; a second failed check yields the text reply; `Fix`
  sends the failed code and stderr and returns the corrected approval, all
  against `geminitest`.
- `internal/workload/server`: a failed run in Gemini mode yields one reply
  with `Corrected` set and a new pending approval; a failed fix gets the
  plain reply with no model call; a `Fix` error gets the plain reply; mock
  mode and successful runs are unchanged.
- `cmd/conf-gateway`: a reply to `/api/approve` carrying an approval becomes
  pending and sets the activity to awaiting approval; a `Corrected` execution
  does not update `lastResults`; static tests cover the new rendering hooks.
- `make verify-local` still passes.

## Risks

- A fix doubles the model calls for that request, within the gateway's
  existing per-request cap and per-actor in-flight limit.
- `sys.stdlib_module_names` includes modules the image may not ship (for
  example `tkinter`). They pass the check and fail at runtime, where the fix
  turn catches them.
- A model that keeps reaching for pandas will hit the text reply more often
  than before, which is the intended trade: no approval for code we know
  cannot run.
