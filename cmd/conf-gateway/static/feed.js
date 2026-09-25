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

(function() {
  'use strict';

  // DOM Elements
  const el = {
    actionStream: document.getElementById('action-stream'),
    feedSearch: document.getElementById('feed-search'),
    feedSearchClear: document.getElementById('feed-search-clear'),
    feedAutoscroll: document.getElementById('feed-autoscroll'),
    btnResetAll: document.getElementById('btn-reset-all'),
    parkingBadge: document.getElementById('parking-badge'),
    parkingCount: document.getElementById('parking-count'),
    filterButtons: document.querySelectorAll('.btn-filter'),
  };

  // State
  let currentFilter = 'all';
  let searchQuery = '';
  const MAX_CARDS = 100;
  let eventSource = null;
  let parkingPollTimer = null;

  // -------------------------------------------------------------------------
  // HTML & String Utilities
  // -------------------------------------------------------------------------
  function escapeHtml(str) {
    if (str == null) return '';
    return String(str)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function escapeAttribute(str) {
    if (str == null) return '';
    return String(str)
      .replace(/&/g, '&amp;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;');
  }

  function formatTimestamp(ts) {
    if (!ts) return 'Just now';
    const date = new Date(ts * 1000);
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }

  // -------------------------------------------------------------------------
  // Pure-JS Single-Pass Syntax Highlighter for Python & Bash
  // Completely self-contained, zero external CDN / library dependencies.
  // -------------------------------------------------------------------------
  function highlightCode(code, lang) {
    if (!code) return '';

    const commentRegex = /#[^\n]*/;
    const strRegex = /("""[\s\S]*?"""|'''[\s\S]*?'''|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')/;
    const numRegex = /\b\d+(?:\.\d+)?\b/;

    const pyKeywords = /\b(def|class|return|if|elif|else|while|for|in|try|except|finally|with|as|import|from|pass|break|continue|lambda|yield|async|await|and|or|not|is|raise|global|nonlocal|assert|del|True|False|None)\b/;
    const bashKeywords = /\b(if|then|else|elif|fi|case|esac|for|while|until|do|done|in|function|select|time|echo|export|exit|source|grep|sed|awk|curl|jq|kubectl|sudo|chmod|cat|ls|rm|mkdir)\b/;

    const isBash = (lang || '').toLowerCase().includes('bash') || (lang || '').toLowerCase().includes('sh');
    const kwRegex = isBash ? bashKeywords : pyKeywords;

    const tokenRegex = new RegExp(
      [
        strRegex.source,
        commentRegex.source,
        kwRegex.source,
        numRegex.source,
      ].join('|'),
      'g'
    );

    let lastIndex = 0;
    let out = '';
    let match;

    while ((match = tokenRegex.exec(code)) !== null) {
      const matchIndex = match.index;
      if (matchIndex > lastIndex) {
        out += escapeHtml(code.slice(lastIndex, matchIndex));
      }
      const token = match[0];
      if (token.startsWith('#')) {
        out += `<span class="tok-comment">${escapeHtml(token)}</span>`;
      } else if (token.startsWith('"') || token.startsWith("'")) {
        out += `<span class="tok-string">${escapeHtml(token)}</span>`;
      } else if (/^\d/.test(token)) {
        out += `<span class="tok-number">${escapeHtml(token)}</span>`;
      } else {
        out += `<span class="tok-keyword">${escapeHtml(token)}</span>`;
      }
      lastIndex = tokenRegex.lastIndex;
    }

    if (lastIndex < code.length) {
      out += escapeHtml(code.slice(lastIndex));
    }
    return out;
  }

  // -------------------------------------------------------------------------
  // Card Rendering & Lifecycle
  // -------------------------------------------------------------------------
  // getActivityHTML renders what the gateway actually observed for this
  // entry: the actor's activity, the reply source when it wasn't Gemini,
  // and the worker pod that ran it.
  function getActivityHTML(entry) {
    const a = window.ActorView.activity({ activity: entry.activity });
    const src = window.ActorView.source(entry.source);
    let html = a ? `<span class="status-pill ${a.cls}">${escapeHtml(a.label)}</span>` : '';
    if (src) html += `<span class="source-tag ${src.cls}">${escapeHtml(src.label)}</span>`;
    if (entry.worker_pod) {
      html += `<span class="footprint-pill" title="Worker pod">${escapeHtml(entry.worker_pod)}</span>`;
    }
    return html;
  }

  function renderCardInner(entry) {
    const attendeeId = entry.attendee_id || 'unknown';
    const attendeeNum = attendeeId.replace('attendee-', '');

    // Risk Pill
    let riskHTML = '';
    if (entry.risk_level) {
      const rk = ['low', 'medium', 'high'].includes(entry.risk_level.toLowerCase()) ? entry.risk_level.toLowerCase() : 'high';
      riskHTML = `<span class="risk-pill ${rk}">${escapeHtml(entry.risk_level)} RISK</span>`;
    }

    // Prompt Section
    let promptHTML = '';
    if (entry.prompt) {
      promptHTML = `
        <div class="card-prompt-section">
          <span class="section-label">User Prompt</span>
          <div class="card-prompt-text">${escapeHtml(entry.prompt)}</div>
        </div>
      `;
    }

    // Model Response Section (AI reasoning / conversational summary)
    let responseHTML = '';
    if (entry.model_response && !entry.model_response.startsWith('Execution completed in ')) {
      responseHTML = `
        <div class="card-response-section">
          <span class="section-label">AI Reasoning &amp; Plan</span>
          <div class="card-response-text">${escapeHtml(entry.model_response)}</div>
        </div>
      `;
    }

    // Code Block Section
    let codeHTML = '';
    if (entry.code) {
      const lang = entry.language || 'python';
      codeHTML = `
        <div class="card-code-section">
          <div class="code-header-bar">
            <span class="code-lang-tag">${escapeHtml(lang)}</span>
            <div class="code-actions">
              <button type="button" class="btn-copy-code">
                📋 Copy Code
              </button>
            </div>
          </div>
          <pre class="code-pre"><code>${highlightCode(entry.code, lang)}</code></pre>
        </div>
      `;
    }

    // Execution Terminal Section. A failed run shows one line, not a dump.
    let terminalHTML = '';
    const failure = window.ActorView.run(entry.execution);
    if (failure) {
      terminalHTML = `
        <div class="card-terminal-section">
          <div class="terminal-header-bar">
            <span class="terminal-title">💻 gVisor Sandbox Output</span>
            <div class="terminal-metrics">
              <span class="term-exit-status ${failure.cls}">${escapeHtml(failure.label)}</span>
            </div>
          </div>
          <div class="terminal-body">
            ${failure.detail ? `<div class="term-detail">${escapeHtml(failure.detail)}</div>` : ''}
          </div>
        </div>
      `;
    } else if (entry.execution) {
      terminalHTML = `
        <div class="card-terminal-section">
          <div class="terminal-header-bar">
            <span class="terminal-title">💻 gVisor Sandbox Output</span>
            <div class="terminal-metrics">
              <span class="term-exit-status exit-0">Exit ${entry.execution.exit_code}</span>
              <span class="term-duration">${entry.execution.duration_ms}ms</span>
            </div>
          </div>
          <div class="terminal-body">
            ${entry.execution.stdout ? `<div class="term-stdout">${escapeHtml(entry.execution.stdout)}</div>` : ''}
            ${entry.execution.stderr ? `<div class="term-stderr">${escapeHtml(entry.execution.stderr)}</div>` : ''}
          </div>
        </div>
      `;
    }

    return `
      <div class="card-header-row">
        <div class="card-attendee-meta">
          <a href="/chat?attendee=${encodeURIComponent(attendeeId)}" target="_blank" rel="noopener noreferrer" class="attendee-link" title="Open chat session in new tab">
            <span class="attendee-glyph" aria-hidden="true">👤</span> Attendee #${escapeHtml(attendeeNum)}
          </a>
          <span class="card-timestamp">${formatTimestamp(entry.timestamp)}</span>
        </div>
        <div class="card-badges">
          ${getActivityHTML(entry)}
          ${riskHTML}
        </div>
      </div>

      ${promptHTML}
      ${responseHTML}
      ${codeHTML}
      ${terminalHTML}

      <div class="card-footer-row">
        <div class="footer-left">
          <span>Action ID: <code>${escapeHtml(entry.id)}</code></span>
        </div>
        <div class="footer-right">
          <span>Target: <code>ate-demo-conf/${escapeHtml(attendeeId)}</code></span>
        </div>
      </div>
    `;
  }

  function renderOrUpdateCard(entry, skipScroll) {
    if (!el.actionStream || !entry || !entry.id) return;

    // Clear empty state placeholder if present
    const emptyState = document.getElementById('feed-empty-state');
    if (emptyState) {
      emptyState.remove();
    }

    // Clear clean slate banner if present
    const cleanSlate = el.actionStream.querySelector('.clean-slate-banner');
    if (cleanSlate) {
      cleanSlate.remove();
    }

    const cardId = 'action-' + entry.id;
    let card = document.getElementById(cardId);

    if (card) {
      // In-place update
      card.dataset.activity = entry.activity || '';
      card.dataset.attendee = entry.attendee_id || '';
      card.innerHTML = renderCardInner(entry);
    } else {
      // Create new card and prepend to top
      card = document.createElement('article');
      card.className = 'action-card';
      card.id = cardId;
      card.dataset.id = entry.id;
      card.dataset.activity = entry.activity || '';
      card.dataset.attendee = entry.attendee_id || '';
      card.innerHTML = renderCardInner(entry);

      el.actionStream.prepend(card);

      // Memory Bounding: maintain max MAX_CARDS in DOM
      while (el.actionStream.children.length > MAX_CARDS) {
        el.actionStream.removeChild(el.actionStream.lastElementChild);
      }

      // Auto-scroll to top when enabled
      if (!skipScroll && el.feedAutoscroll && el.feedAutoscroll.checked) {
        window.scrollTo({ top: 0, behavior: 'smooth' });
      }
    }

    // Apply active filter and search visibility to this card
    updateCardVisibility(card);
  }

  // -------------------------------------------------------------------------
  // Filtering and Real-Time Search
  // -------------------------------------------------------------------------
  function updateCardVisibility(card) {
    const activity = (card.dataset.activity || '').toUpperCase();
    const hasExecution = !!card.querySelector('.card-terminal-section');

    let stateMatch = false;
    if (currentFilter === 'all') {
      stateMatch = true;
    } else if (currentFilter === 'thinking') {
      stateMatch = (activity === 'THINKING');
    } else if (currentFilter === 'awaiting') {
      stateMatch = (activity === 'AWAITING_APPROVAL');
    } else if (currentFilter === 'executed') {
      stateMatch = hasExecution;
    }

    let searchMatch = true;
    if (searchQuery) {
      const cardText = card.textContent.toLowerCase();
      searchMatch = cardText.includes(searchQuery);
    }

    if (stateMatch && searchMatch) {
      card.style.display = '';
    } else {
      card.style.display = 'none';
    }
  }

  function applyAllFilters() {
    if (!el.actionStream) return;
    const cards = el.actionStream.querySelectorAll('.action-card');
    cards.forEach(card => updateCardVisibility(card));
  }

  // -------------------------------------------------------------------------
  // Clipboard Copy Helper
  // -------------------------------------------------------------------------
  function copyTextToClipboard(text, btn) {
    const orig = btn.innerHTML;
    function setSuccess() {
      btn.innerHTML = '✓ Copied!';
      btn.style.borderColor = 'var(--neon-cyan)';
      btn.style.color = 'var(--neon-cyan)';
      setTimeout(() => {
        btn.innerHTML = orig;
        btn.style.borderColor = '';
        btn.style.color = '';
      }, 1500);
    }

    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(setSuccess).catch(() => {
        fallbackCopy(text, setSuccess);
      });
    } else {
      fallbackCopy(text, setSuccess);
    }
  }

  function fallbackCopy(text, cb) {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.focus();
    ta.select();
    try {
      document.execCommand('copy');
      if (cb) cb();
    } catch (e) {
      console.warn('Fallback copy failed', e);
    }
    document.body.removeChild(ta);
  }

  // -------------------------------------------------------------------------
  // Live SSE Event Stream Connection
  // -------------------------------------------------------------------------
  function connectStream() {
    eventSource = new EventSource('/api/stream');

    eventSource.addEventListener('action_entry', (e) => {
      try {
        const entry = JSON.parse(e.data);
        renderOrUpdateCard(entry);
      } catch (err) {
        console.error('Failed to parse action_entry JSON:', err);
      }
    });

    eventSource.addEventListener('reset', () => {
      handleClusterReset();
    });

    eventSource.onerror = (err) => {
      console.warn('Stream SSE connection interrupted, retrying...', err);
    };
  }

  function handleClusterReset() {
    if (!el.actionStream) return;
    el.actionStream.innerHTML = `
      <div class="clean-slate-banner">
        <div class="banner-icon" aria-hidden="true">⟲</div>
        <div class="banner-body">
          <h2 class="banner-title">Cluster Reset Complete</h2>
          <p class="banner-desc">All actors reset. Worker pods and buffer slots freed. Clean slate restored.</p>
        </div>
      </div>
    `;
    if (el.parkingCount) el.parkingCount.textContent = '0';
    if (el.parkingBadge) el.parkingBadge.classList.remove('has-parked');
  }

  // -------------------------------------------------------------------------
  // Initial Feed History Fetch
  // -------------------------------------------------------------------------
  async function fetchFeedHistory() {
    try {
      const resp = await fetch('/api/feed/history', { cache: 'no-store' });
      if (!resp.ok) return;
      const history = await resp.json();
      if (Array.isArray(history) && history.length > 0) {
        // history is in chronological order (oldest to newest)
        // rendering each sequentially with prepend puts newest at the top
        history.forEach(entry => renderOrUpdateCard(entry, true));
        window.scrollTo({ top: 0 });
      }
    } catch (err) {
      console.error('Failed to fetch feed history:', err);
    }
  }

  // -------------------------------------------------------------------------
  // Periodic Envoy Parking Badge Polling
  // -------------------------------------------------------------------------
  async function pollParkingBadge() {
    try {
      const resp = await fetch('/api/stage/telemetry', { cache: 'no-store' });
      if (!resp.ok) return;
      const telem = await resp.json();
      const parked = telem.parked_requests || 0;
      if (el.parkingCount) {
        el.parkingCount.textContent = parked;
      }
      if (el.parkingBadge) {
        if (parked > 0) {
          el.parkingBadge.classList.add('has-parked');
        } else {
          el.parkingBadge.classList.remove('has-parked');
        }
      }
    } catch (err) {
      // Quiet background polling error
    }
  }

  // -------------------------------------------------------------------------
  // Reset Button & Event Handlers
  // -------------------------------------------------------------------------
  function initHandlers() {
    // Copy code button delegation
    if (el.actionStream) {
      el.actionStream.addEventListener('click', (e) => {
        const copyBtn = e.target.closest('.btn-copy-code');
        if (copyBtn) {
          const section = copyBtn.closest('.card-code-section');
          if (section) {
            const codeEl = section.querySelector('code');
            if (codeEl) {
              copyTextToClipboard(codeEl.textContent, copyBtn);
            }
          }
        }
      });
    }

    // Filter Buttons
    if (el.filterButtons) {
      el.filterButtons.forEach(btn => {
        btn.addEventListener('click', () => {
          el.filterButtons.forEach(b => {
            b.classList.remove('active');
            b.setAttribute('aria-pressed', 'false');
          });
          btn.classList.add('active');
          btn.setAttribute('aria-pressed', 'true');
          currentFilter = btn.dataset.filter || 'all';
          applyAllFilters();
        });
      });
    }

    // Search Input
    if (el.feedSearch) {
      el.feedSearch.addEventListener('input', () => {
        searchQuery = el.feedSearch.value.trim().toLowerCase();
        if (el.feedSearchClear) {
          el.feedSearchClear.style.display = searchQuery ? 'inline-block' : 'none';
        }
        applyAllFilters();
      });
    }

    if (el.feedSearchClear) {
      el.feedSearchClear.addEventListener('click', () => {
        if (el.feedSearch) {
          el.feedSearch.value = '';
          searchQuery = '';
          el.feedSearchClear.style.display = 'none';
          el.feedSearch.focus();
          applyAllFilters();
        }
      });
    }

    // Reset All Button
    if (el.btnResetAll) {
      el.btnResetAll.addEventListener('click', async () => {
        const origText = el.btnResetAll.innerHTML;
        el.btnResetAll.innerHTML = '<span class="btn-icon">⟲</span> Resetting...';
        el.btnResetAll.disabled = true;

        try {
          const resp = await fetch('/api/reset', { method: 'POST' });
          if (resp.ok) {
            el.btnResetAll.innerHTML = '<span class="btn-icon">✓</span> Reset Complete';
          } else {
            el.btnResetAll.innerHTML = '<span class="btn-icon">✕</span> Reset Failed';
          }
        } catch (err) {
          el.btnResetAll.innerHTML = '<span class="btn-icon">✕</span> Reset Failed';
        } finally {
          setTimeout(() => {
            if (el.btnResetAll) {
              el.btnResetAll.innerHTML = origText;
              el.btnResetAll.disabled = false;
            }
          }, 1500);
        }
      });
    }
  }

  // -------------------------------------------------------------------------
  // Initialization & Lifecycle Cleanup
  // -------------------------------------------------------------------------
  window.addEventListener('beforeunload', function() {
    if (eventSource) eventSource.close();
    if (parkingPollTimer) clearInterval(parkingPollTimer);
  });

  document.addEventListener('DOMContentLoaded', () => {
    initHandlers();
    fetchFeedHistory();
    connectStream();
    pollParkingBadge();
    parkingPollTimer = setInterval(pollParkingBadge, 1000);
  });
})();
