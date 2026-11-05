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

  // volume maps an ActorView's external volume fields to how the spotlight
  // drawer presents the actor's persistent /workspace mount and whether the
  // Filestore starter prompt applies on /chat.
  function volume(view) {
    const isFilestore = Boolean(
      view && (
        (view.volume_type || '').indexOf('filestore') !== -1 ||
        (view.volume_uri || '').indexOf('volumepool://') !== -1
      )
    );
    if (view && view.volume_share) {
      return {
        label: view.volume_share,
        sub: '/workspace',
        uri: view.volume_uri || view.volume_share,
        bound: true,
        configured: true,
        filestore: isFilestore
      };
    }
    if (view && view.volume_type) {
      return {
        label: 'Provisioned on first wake',
        sub: view.volume_type,
        uri: view.volume_type,
        bound: false,
        configured: true,
        filestore: isFilestore
      };
    }
    return {
      label: 'Container rootfs',
      sub: '/tmp/workspace',
      uri: '',
      bound: false,
      configured: false,
      filestore: false
    };
  }

  // fleetVolumes summarizes external volume bindings across reported actors
  // for the stage header spec card.
  function fleetVolumes(actorsMap) {
    const ids = fleetIDs(actorsMap);
    if (ids.length === 0) {
      return { known: false, configured: false, label: '--', sub: '', bound: 0, total: 0 };
    }
    let configured = 0;
    let bound = 0;
    let driver = '';
    for (let i = 0; i < ids.length; i++) {
      const v = actorsMap[ids[i]] || {};
      if (v.volume_type || v.volume_share) {
        configured++;
        if (!driver && v.volume_type) driver = v.volume_type;
      }
      if (v.volume_share) bound++;
    }
    if (configured === 0) {
      return {
        known: true,
        configured: false,
        label: 'Container rootfs',
        sub: '/tmp/workspace',
        bound: 0,
        total: ids.length
      };
    }
    const isFilestore = driver.indexOf('filestore') !== -1;
    return {
      known: true,
      configured: true,
      label: isFilestore ? 'Filestore Agent Volumes' : (driver || 'External CSI Volumes'),
      sub: bound + ' of ' + ids.length + ' /workspace shares bound',
      bound: bound,
      total: ids.length
    };
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

  // tokenUsable reports whether an attendee JWT is structurally valid, names
  // attendeeId, and has more than 60 seconds left before expiry.
  function tokenUsable(token, attendeeId, nowMs) {
    if (!token || typeof token !== 'string' || !attendeeId) return false;
    const parts = token.split('.');
    if (parts.length !== 3) return false;
    try {
      let b64 = parts[1].replace(/-/g, '+').replace(/_/g, '/');
      while (b64.length % 4 !== 0) b64 += '=';
      const raw = typeof atob === 'function'
        ? atob(b64)
        : Buffer.from(b64, 'base64').toString('utf8');
      const claims = JSON.parse(raw);
      if (!claims || claims.sub !== attendeeId || typeof claims.exp !== 'number') {
        return false;
      }
      const now = typeof nowMs === 'number' ? nowMs : Date.now();
      return claims.exp * 1000 > now + 60000;
    } catch (_) {
      return false;
    }
  }

  const THEME_KEY = 'conf_theme';

  function getTheme() {
    try {
      if (typeof localStorage !== 'undefined' && localStorage.getItem(THEME_KEY) === 'light') {
        return 'light';
      }
    } catch (_) {}
    if (typeof document !== 'undefined' && document.documentElement &&
        document.documentElement.getAttribute('data-theme') === 'light') {
      return 'light';
    }
    return 'dark';
  }

  function syncThemeToggleUI(isLight) {
    if (typeof document === 'undefined' || !document.getElementById) return;
    const darkSeg = document.getElementById('theme-seg-dark');
    const lightSeg = document.getElementById('theme-seg-light');
    const btn = document.getElementById('btn-theme-toggle');
    if (darkSeg) darkSeg.classList.toggle('active', !isLight);
    if (lightSeg) lightSeg.classList.toggle('active', isLight);
    if (btn) btn.setAttribute('aria-pressed', isLight ? 'true' : 'false');
  }

  function setTheme(theme) {
    const isLight = theme === 'light';
    if (typeof document !== 'undefined' && document.documentElement) {
      if (isLight) {
        document.documentElement.setAttribute('data-theme', 'light');
      } else {
        document.documentElement.removeAttribute('data-theme');
      }
    }
    try {
      if (typeof localStorage !== 'undefined') {
        localStorage.setItem(THEME_KEY, isLight ? 'light' : 'dark');
      }
    } catch (_) {}
    syncThemeToggleUI(isLight);
    return isLight ? 'light' : 'dark';
  }

  function toggleTheme() {
    return setTheme(getTheme() === 'light' ? 'dark' : 'light');
  }

  function bindThemeToggle() {
    if (typeof document === 'undefined' || !document.getElementById) return;
    syncThemeToggleUI(getTheme() === 'light');
    const btn = document.getElementById('btn-theme-toggle');
    if (btn && !btn.dataset.themeBound) {
      btn.dataset.themeBound = '1';
      btn.addEventListener('click', function() {
        toggleTheme();
      });
    }
  }

  if (typeof document !== 'undefined' && document.documentElement) {
    if (getTheme() === 'light') {
      document.documentElement.setAttribute('data-theme', 'light');
    } else {
      document.documentElement.removeAttribute('data-theme');
    }
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', bindThemeToggle);
    } else {
      bindThemeToggle();
    }
    if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
      window.addEventListener('storage', function(e) {
        if (e && e.key === THEME_KEY) {
          const isLight = e.newValue === 'light';
          if (isLight) {
            document.documentElement.setAttribute('data-theme', 'light');
          } else {
            document.documentElement.removeAttribute('data-theme');
          }
          syncThemeToggleUI(isLight);
        }
      });
      window.addEventListener('keydown', function(e) {
        if (!e || e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey) return;
        const tag = (e.target && e.target.tagName) ? e.target.tagName.toUpperCase() : '';
        if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || (e.target && e.target.isContentEditable)) return;
        if (e.key === 't' || e.key === 'T') {
          toggleTheme();
        }
      });
    }
  }

  window.ActorView = {
    state: state,
    activity: activity,
    source: source,
    badge: badge,
    fleetIDs: fleetIDs,
    volume: volume,
    fleetVolumes: fleetVolumes,
    run: run,
    tokenUsable: tokenUsable,
    getTheme: getTheme,
    setTheme: setTheme,
    toggleTheme: toggleTheme
  };
})();
