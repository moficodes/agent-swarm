// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Shared mapping from the gateway's ActorView to display labels. Labels use
// ateapi's state names and claim nothing the gateway does not observe.
(function() {
  'use strict';

  const STATES = {
    RESUMING: { label: 'Resuming', sub: 'Restoring on a worker', cls: 'resuming' },
    RUNNING: { label: 'Running', sub: '', cls: 'running' },
    SUSPENDING: { label: 'Suspending', sub: 'Writing snapshot', cls: 'suspending' },
    SUSPENDED: { label: 'Suspended', sub: 'Snapshot saved', cls: 'suspended' },
    PAUSING: { label: 'Pausing', sub: '', cls: 'paused' },
    PAUSED: { label: 'Paused', sub: '', cls: 'paused' },
    CRASHED: { label: 'Crashed', sub: '', cls: 'crashed' },
    DELETING: { label: 'Deleting', sub: '', cls: 'transition' },
    REVERTING: { label: 'Reverting', sub: '', cls: 'transition' },
    UNAVAILABLE: { label: 'State unavailable', sub: '', cls: 'unavailable' },
  };
  const UNKNOWN = { label: 'Unknown', sub: '', cls: 'unknown' };

  const ACTIVITIES = {
    THINKING: { label: 'Thinking', cls: 'thinking' },
    AWAITING_APPROVAL: { label: 'Awaiting approval', cls: 'awaiting' },
    EXECUTING: { label: 'Executing', cls: 'executing' },
  };

  const SOURCES = {
    'mock': { label: 'Mock reply', cls: 'mock' },
    'mock-fallback': { label: 'Mock (model error)', cls: 'mock-fallback' },
  };

  // state maps an ActorView to a badge {label, sub, cls}. A missing or
  // unrecognized state renders as a neutral "Unknown" badge rather than an
  // error, since UNKNOWN is the actor's real state before the first poll.
  function state(view) {
    if (!view) return UNKNOWN;
    if (view.never_started) {
      return { label: 'Never started', sub: 'No snapshot yet', cls: 'never-started' };
    }
    const base = STATES[view.state] || UNKNOWN;
    let sub = base.sub;
    if (view.state === 'RUNNING') sub = view.worker_pod || '';
    if (['CRASHED', 'UNAVAILABLE', 'UNKNOWN'].includes(view.state) || !STATES[view.state]) {
      sub = view.state_detail || '';
    }
    return { label: base.label, sub: sub, cls: base.cls };
  }

  // activity maps an ActorView's activity to a chip {label, cls}, or null
  // when the actor is idle.
  function activity(view) {
    return (view && ACTIVITIES[view.activity]) || null;
  }

  // source maps a ReplySource to a tag {label, cls}, or null for a real
  // Gemini reply, which needs no tag.
  function source(src) {
    return SOURCES[src] || null;
  }

  // badge maps an ActorView to the fleet grid's {label, sub, cls}. What the
  // actor is doing for its attendee leads, since "Awaiting approval" is the
  // one that needs a human; the substrate state moves to the sub line. A
  // crash or unreadable state outranks activity.
  function badge(view) {
    const s = state(view);
    if (view && ['CRASHED', 'UNAVAILABLE'].includes(view.state)) {
      return { label: s.label, sub: s.sub, cls: 'problem' };
    }
    const a = activity(view);
    if (a) return { label: a.label, sub: s.label, cls: a.cls };
    return s;
  }

  // fleetIDs returns the actor IDs in a telemetry actors map, sorted.
  function fleetIDs(actorsMap) {
    return Object.keys(actorsMap || {}).sort();
  }

  // run maps an execution result to how pages present a failed run: a chip
  // {label, cls} and one line of detail, the last line of stderr, which for
  // a Python traceback is the exception. A successful run maps to null.
  function run(exec) {
    if (!exec || (exec.exit_code === 0 && !exec.corrected)) return null;
    const lines = (exec.stderr || '').trim().split('\n');
    let detail = lines[lines.length - 1].trim();
    // The sandbox's own timeout, not the script, ended the run: say so
    // plainly instead of showing Go's context-deadline text.
    if (exec.exit_code === -1 && (exec.stderr || '').includes('deadline exceeded')) {
      detail = 'Stopped after the 5 second limit';
    }
    if (exec.corrected) return { label: 'Self-corrected', cls: 'run-corrected', detail: detail };
    return { label: "Didn't run", cls: 'run-failed', detail: detail };
  }

  window.ActorView = { state: state, activity: activity, source: source, badge: badge, fleetIDs: fleetIDs, run: run };
})();
