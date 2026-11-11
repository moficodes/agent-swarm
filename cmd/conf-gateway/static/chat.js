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

  // Application State
  const state = {
    attendeeId: '',
    token: '',
    inFlight: false,
    eventSource: null,
  };

  // DOM Elements
  const el = {
    chatFeed: document.getElementById('chat-feed'),
    chatForm: document.getElementById('chat-form'),
    promptInput: document.getElementById('prompt-input'),
    sendBtn: document.getElementById('send-btn'),
    attendeeBadge: document.getElementById('attendee-badge'),
    statusPill: document.getElementById('status-pill'),
    statusText: document.getElementById('status-text'),
    statusSub: document.getElementById('status-sub'),
    activityChip: document.getElementById('activity-chip'),
    filestoreChip: document.getElementById('chip-filestore'),
    approvalTemplate: document.getElementById('approval-card-template'),
  };

  // renderActorView applies an ActorView pushed over SSE to the status pill,
  // activity chip, and Filestore-only starter prompt chip. Views for another
  // attendee (e.g. a stale event from a previous session) are ignored.
  function renderActorView(view) {
    if (!view || (state.attendeeId && view.actor_id !== state.attendeeId)) return;
    const s = window.ActorView.state(view);
    if (el.statusPill) el.statusPill.className = 'status-pill ' + s.cls;
    if (el.statusText) el.statusText.textContent = s.label;
    if (el.statusSub) el.statusSub.textContent = s.sub;
    const a = window.ActorView.activity(view);
    if (el.activityChip) {
      el.activityChip.hidden = !a;
      if (a) {
        el.activityChip.className = 'activity-chip ' + a.cls;
        el.activityChip.textContent = a.label;
      }
    }
    if (el.filestoreChip) {
      el.filestoreChip.hidden = !window.ActorView.volume(view).filestore;
    }
  }

  // -------------------------------------------------------------------------
  // Server-Sent Events (SSE) Real-Time State Sync
  // -------------------------------------------------------------------------
  function connectEventSource(allowRefreshOnClose) {
    if (!state.attendeeId || !state.token) return;
    if (state.eventSource) {
      state.eventSource.close();
      state.eventSource = null;
    }

    // EventSource cannot send an Authorization header, so the token rides in the query.
    const es = new EventSource('/api/events?attendee_id=' + encodeURIComponent(state.attendeeId) +
      '&token=' + encodeURIComponent(state.token));
    state.eventSource = es;
    let sseOpened = false;

    es.onopen = function() {
      sseOpened = true;
    };

    es.addEventListener('actor_view', function(e) {
      sseOpened = true;
      try {
        renderActorView(JSON.parse(e.data));
      } catch (err) {
        console.warn('Failed to parse actor_view event:', err);
      }
    });

    es.addEventListener('reset', function(e) {
      handleResetEvent();
    });

    es.onerror = function() {
      // A 401/403 handshake rejection permanently closes EventSource
      // (readyState === 2) before onopen fires. By contrast, if the stream was
      // already open and later closed (e.g. mobile browser sleep or gateway rollout),
      // keep the current attendee session and reconnect the SSE stream.
      if (es.readyState === 2 && state.eventSource === es) {
        es.close();
        state.eventSource = null;
        if (sseOpened && window.ActorView.tokenUsable(state.token, state.attendeeId)) {
          setTimeout(function() {
            connectEventSource(allowRefreshOnClose);
          }, 1500);
        } else if (allowRefreshOnClose) {
          initSession(true);
        }
      }
    };
  }

  function handleResetEvent() {
    state.inFlight = false;
    setInputState(false);
    removeTypingIndicator();

    // The next actor_view event restores the badge.

    if (el.chatFeed) {
      const intro = el.chatFeed.querySelector('.intro-message');
      el.chatFeed.innerHTML = '';
      if (intro) {
        el.chatFeed.appendChild(intro);
      }
      appendSystemMessage('⟲ Cluster reset: session state and process memory returned to zero.');
    }
  }

  function formatAttendeeBadge(id) {
    if (!id) return 'Attendee #--';
    // Match pattern like attendee-14 -> Attendee #14
    const match = id.match(/attendee-(\d+)/i);
    if (match) {
      return 'Attendee #' + match[1];
    }
    return id;
  }

  function escapeHTML(str) {
    if (!str) return '';
    return String(str)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  // Lightweight Offline Single-Pass Syntax Highlighter for Python / Shell
  function highlightCode(code) {
    if (!code) return '';
    const tokenRegex = /(#.*$)|("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')|(\b(?:def|class|return|import|from|if|elif|else|for|while|try|except|finally|with|as|pass|break|continue|lambda|yield|in|is|not|and|or|print|None|True|False)\b)|(\b[a-zA-Z_][a-zA-Z0-9_]*(?=\s*\())|(\b\d+(?:\.\d+)?\b)/gm;
    let result = '';
    let lastIndex = 0;
    let match;

    while ((match = tokenRegex.exec(code)) !== null) {
      if (match.index > lastIndex) {
        result += escapeHTML(code.slice(lastIndex, match.index));
      }
      const [full, comment, string, keyword, fn, number] = match;
      if (comment) {
        result += '<span class="syn-com">' + escapeHTML(comment) + '</span>';
      } else if (string) {
        result += '<span class="syn-str">' + escapeHTML(string) + '</span>';
      } else if (keyword) {
        result += '<span class="syn-kwd">' + escapeHTML(keyword) + '</span>';
      } else if (fn) {
        result += '<span class="syn-fn">' + escapeHTML(fn) + '</span>';
      } else if (number) {
        result += '<span class="syn-num">' + escapeHTML(number) + '</span>';
      } else {
        result += escapeHTML(full);
      }
      lastIndex = tokenRegex.lastIndex;
    }

    if (lastIndex < code.length) {
      result += escapeHTML(code.slice(lastIndex));
    }
    return result;
  }

  function scrollToBottom() {
    if (el.chatFeed) {
      el.chatFeed.scrollTop = el.chatFeed.scrollHeight;
    }
  }

  function formatTime(timestamp) {
    if (!timestamp) return 'Now';
    const d = new Date(timestamp > 10000000000 ? timestamp : timestamp * 1000);
    return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }

  // Session Management
  async function initSession(forceRefresh) {
    const storedId = localStorage.getItem('conf_attendee_id');
    const storedToken = localStorage.getItem('conf_attendee_token');

    // If query string has reset (or an SSE/HTTP 401 forced refresh), clear stored session
    const urlParams = new URLSearchParams(window.location.search);
    const forceReset = forceRefresh === true || urlParams.get('reset') === '1';
    const attendeeParam = urlParams.get('attendee') || urlParams.get('attendee_id');

    if (attendeeParam) {
      state.attendeeId = attendeeParam;
      if (!forceReset && storedId === attendeeParam && window.ActorView.tokenUsable(storedToken, attendeeParam)) {
        state.token = storedToken;
        if (el.attendeeBadge) el.attendeeBadge.textContent = formatAttendeeBadge(state.attendeeId);
        connectEventSource(true);
        return;
      }
      if (el.attendeeBadge) el.attendeeBadge.textContent = formatAttendeeBadge(state.attendeeId);
    } else if (!forceReset && storedId && window.ActorView.tokenUsable(storedToken, storedId)) {
      state.attendeeId = storedId;
      state.token = storedToken;
      if (el.attendeeBadge) el.attendeeBadge.textContent = formatAttendeeBadge(state.attendeeId);
      connectEventSource(true);
      return;
    }

    localStorage.removeItem('conf_attendee_token');
    state.token = '';

    try {
      const sessionURL = attendeeParam ? ('/api/session?attendee_id=' + encodeURIComponent(attendeeParam)) : '/api/session';
      const resp = await fetch(sessionURL, { method: 'GET' });
      if (!resp.ok) {
        throw new Error('HTTP ' + resp.status + ' initializing session');
      }
      const data = await resp.json();
      state.attendeeId = data.attendee_id;
      localStorage.setItem('conf_attendee_id', state.attendeeId);
      state.token = data.token;
      localStorage.setItem('conf_attendee_token', state.token);

      if (el.attendeeBadge) el.attendeeBadge.textContent = formatAttendeeBadge(state.attendeeId);
      connectEventSource(false);
    } catch (err) {
      console.error('Failed to init session:', err);
      appendSystemMessage('Unable to connect to gateway session. Please refresh the page.');
    }
  }

  // Feed Rendering
  function appendMessage(sender, content, timestamp, source) {
    const msgDiv = document.createElement('div');
    msgDiv.className = 'chat-message ' + (sender === 'user' ? 'user-message' : 'assistant-message');

    const metaDiv = document.createElement('div');
    metaDiv.className = 'message-meta';

    const senderSpan = document.createElement('span');
    senderSpan.className = 'sender-label';
    senderSpan.textContent = sender === 'user' ? formatAttendeeBadge(state.attendeeId) : 'Gemini 3.8 Flash';

    const timeSpan = document.createElement('span');
    timeSpan.className = 'timestamp';
    timeSpan.textContent = formatTime(timestamp || Date.now());

    metaDiv.appendChild(senderSpan);
    metaDiv.appendChild(timeSpan);

    if (sender === 'assistant') {
      const src = window.ActorView.source(source);
      if (src) {
        const sourceSpan = document.createElement('span');
        sourceSpan.className = 'source-tag ' + src.cls;
        sourceSpan.textContent = src.label;
        metaDiv.appendChild(sourceSpan);
      }
    }

    const bubbleDiv = document.createElement('div');
    bubbleDiv.className = 'message-bubble';
    
    // Split into paragraphs or preserve newlines
    const paragraphs = (content || '').split('\n\n');
    paragraphs.forEach(function(para) {
      if (para.trim()) {
        const p = document.createElement('p');
        p.textContent = para.trim();
        bubbleDiv.appendChild(p);
      }
    });

    msgDiv.appendChild(metaDiv);
    msgDiv.appendChild(bubbleDiv);

    el.chatFeed.appendChild(msgDiv);
    scrollToBottom();
    return msgDiv;
  }

  function appendSystemMessage(text) {
    const msgDiv = document.createElement('div');
    msgDiv.className = 'chat-message system-message';

    const bubbleDiv = document.createElement('div');
    bubbleDiv.className = 'message-bubble';
    bubbleDiv.textContent = text;

    msgDiv.appendChild(bubbleDiv);
    el.chatFeed.appendChild(msgDiv);
    scrollToBottom();
  }

  // renderReply shows an assistant reply: its text, a successful run's
  // output as it printed, a failed run's stderr collapsed under a note, and
  // any approval it asks for.
  function renderReply(msg) {
    const exec = msg.execution;
    const failure = window.ActorView.run(exec);
    if (exec && !failure) {
      const msgDiv = appendMessage('assistant', 'Ran in ' + exec.duration_ms + ' ms.', msg.timestamp, msg.source);
      appendRunOutput(msgDiv, exec);
      return msgDiv;
    }
    const msgDiv = appendMessage('assistant', msg.content, msg.timestamp, msg.source);
    if (failure) appendRunDetails(msgDiv, failure, exec.stderr);
    if (msg.approval) appendApprovalCard(msg.approval, msgDiv);
    return msgDiv;
  }

  // appendRunOutput shows stdout with its line breaks and spacing kept, so
  // ASCII charts and tables line up, and any stderr (such as a warning)
  // collapsed beneath it.
  function appendRunOutput(msgDiv, exec) {
    const bubble = msgDiv.querySelector('.message-bubble');
    const pre = document.createElement('pre');
    pre.className = 'run-output';
    pre.textContent = (exec.stdout || '').replace(/\s+$/, '') || '(no output)';
    bubble.appendChild(pre);
    if (exec.stderr) {
      const details = document.createElement('details');
      details.className = 'run-details';
      const summary = document.createElement('summary');
      summary.textContent = 'Warnings';
      const err = document.createElement('pre');
      err.textContent = exec.stderr;
      details.appendChild(summary);
      details.appendChild(err);
      bubble.appendChild(details);
    }
    scrollToBottom();
  }

  function appendRunDetails(msgDiv, failure, stderr) {
    const bubble = msgDiv.querySelector('.message-bubble');
    const note = document.createElement('div');
    note.className = 'run-note ' + failure.cls;
    note.textContent = failure.label;
    const details = document.createElement('details');
    details.className = 'run-details';
    const summary = document.createElement('summary');
    summary.textContent = 'What went wrong';
    const pre = document.createElement('pre');
    pre.textContent = stderr || '';
    details.appendChild(summary);
    details.appendChild(pre);
    bubble.appendChild(note);
    bubble.appendChild(details);
  }

  function appendApprovalCard(approval, messageContainer) {
    if (!approval || !el.approvalTemplate) return;

    const clone = el.approvalTemplate.content.cloneNode(true);
    const card = clone.querySelector('.approval-card');
    card.setAttribute('data-approval-id', approval.approval_id);

    // Risk indicator
    const riskIndicator = card.querySelector('.risk-indicator');
    const riskLabel = card.querySelector('.risk-label');
    const risk = (approval.risk_level || 'HIGH').toUpperCase();
    riskIndicator.className = 'risk-indicator ' + (risk === 'LOW' ? 'risk-low' : risk === 'MEDIUM' ? 'risk-medium' : 'risk-high');
    riskLabel.textContent = risk + ' RISK';

    // Language & Summary
    const langSpan = card.querySelector('.approval-lang');
    if (langSpan) langSpan.textContent = approval.language || 'python';

    const summaryH4 = card.querySelector('.summary-text');
    if (summaryH4) summaryH4.textContent = approval.summary || 'Authorize Code Execution';

    // Code
    const codeContent = card.querySelector('.code-content');
    if (codeContent) {
      codeContent.innerHTML = highlightCode(approval.code || '');
    }

    // Action Buttons
    const btnReject = card.querySelector('.btn-reject');
    const btnApprove = card.querySelector('.btn-approve');

    btnReject.addEventListener('click', function() {
      handleApprovalDecision(approval.approval_id, 'REJECT', card);
    });

    btnApprove.addEventListener('click', function() {
      handleApprovalDecision(approval.approval_id, 'APPROVE', card);
    });

    const bubble = messageContainer.querySelector('.message-bubble');
    if (bubble) {
      bubble.appendChild(card);
    } else {
      messageContainer.appendChild(card);
    }
    scrollToBottom();
  }

  // Show typing indicator
  let typingEl = null;
  function showTypingIndicator() {
    if (typingEl) return;
    typingEl = document.createElement('div');
    typingEl.className = 'typing-indicator';
    typingEl.innerHTML = '<span class="typing-dot"></span><span class="typing-dot"></span><span class="typing-dot"></span>';
    el.chatFeed.appendChild(typingEl);
    scrollToBottom();
  }

  function removeTypingIndicator() {
    if (typingEl && typingEl.parentNode) {
      typingEl.parentNode.removeChild(typingEl);
    }
    typingEl = null;
  }

  function setInputState(disabled) {
    if (el.promptInput) el.promptInput.disabled = disabled;
    if (el.sendBtn) el.sendBtn.disabled = disabled;
    document.querySelectorAll('.chip').forEach(function(chip) {
      chip.disabled = disabled;
    });
  }

  // Chat Submission
  async function submitPrompt(text) {
    if (!text || state.inFlight) return;
    state.inFlight = true;
    setInputState(true);

    appendMessage('user', text);
    if (el.promptInput) el.promptInput.value = '';

    showTypingIndicator();

    try {
      const resp = await fetch('/api/chat', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer ' + state.token,
          'X-Attendee-ID': state.attendeeId,
        },
        body: JSON.stringify({ message: text }),
      });

      removeTypingIndicator();

      if (resp.status === 401) {
        // Token expired or invalid: refresh session
        localStorage.removeItem('conf_attendee_token');
        await initSession(true);
        appendSystemMessage('⚠️ Session was refreshed. Please re-submit your prompt.');
        return;
      }

      if (!resp.ok) {
        const errorText = await resp.text();
        appendSystemMessage('❌ Error from agent gateway: ' + (errorText || resp.statusText));
        return;
      }

      const chatMsg = await resp.json();
      renderReply(chatMsg);
    } catch (err) {
      console.error('Chat error:', err);
      removeTypingIndicator();
      appendSystemMessage('❌ Network communication error: ' + err.message);
    } finally {
      state.inFlight = false;
      setInputState(false);
      if (el.promptInput) el.promptInput.focus();
    }
  }

  // Approval Decision Handling
  async function handleApprovalDecision(approvalId, decision, cardElement) {
    if (!approvalId || state.inFlight) return;

    state.inFlight = true;
    setInputState(true);

    const btnReject = cardElement ? cardElement.querySelector('.btn-reject') : null;
    const btnApprove = cardElement ? cardElement.querySelector('.btn-approve') : null;
    if (btnReject) btnReject.disabled = true;
    if (btnApprove) btnApprove.disabled = true;

    showTypingIndicator();

    try {
      const resp = await fetch('/api/approve', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'Authorization': 'Bearer ' + state.token,
          'X-Attendee-ID': state.attendeeId,
        },
        body: JSON.stringify({
          approval_id: approvalId,
          decision: decision,
        }),
      });

      removeTypingIndicator();

      if (resp.status === 401) {
        localStorage.removeItem('conf_attendee_token');
        await initSession(true);
        appendSystemMessage('⚠️ Session was refreshed. Please re-submit your decision.');
        if (btnReject) btnReject.disabled = false;
        if (btnApprove) btnApprove.disabled = false;
        return;
      }

      if (!resp.ok) {
        const errorText = await resp.text();
        appendSystemMessage('❌ Failed to submit approval: ' + (errorText || resp.statusText));
        if (btnReject) btnReject.disabled = false;
        if (btnApprove) btnApprove.disabled = false;
        return;
      }

      // Replace actions with finalized badge
      if (cardElement) {
        const actionsDiv = cardElement.querySelector('.approval-actions');
        if (actionsDiv) {
          actionsDiv.innerHTML = '<div class="approval-status-badge ' + (decision === 'APPROVE' ? 'approved' : 'rejected') + '">' +
            (decision === 'APPROVE' ? '✓ Approved' : '✕ Rejected') +
            '</div>';
        }
      }

      const resultMsg = await resp.json();
      renderReply(resultMsg);
    } catch (err) {
      console.error('Approval submission error:', err);
      removeTypingIndicator();
      appendSystemMessage('❌ Error submitting decision: ' + err.message);
      if (btnReject) btnReject.disabled = false;
      if (btnApprove) btnApprove.disabled = false;
    } finally {
      state.inFlight = false;
      setInputState(false);
      if (el.promptInput) el.promptInput.focus();
    }
  }

  // Setup Event Listeners
  function setupEvents() {
    if (el.chatForm) {
      el.chatForm.addEventListener('submit', function(e) {
        e.preventDefault();
        const text = (el.promptInput.value || '').trim();
        if (text) {
          submitPrompt(text);
        }
      });
    }

    if (el.promptInput) {
      el.promptInput.addEventListener('keydown', function(e) {
        if (e.key === 'Enter' && !e.shiftKey) {
          e.preventDefault();
          const text = (el.promptInput.value || '').trim();
          if (text) {
            submitPrompt(text);
          }
        }
      });
    }

    // Prompt Chips
    document.querySelectorAll('.chip').forEach(function(chip) {
      chip.addEventListener('click', function() {
        if (state.inFlight) return;
        const prompt = chip.getAttribute('data-prompt');
        if (prompt) {
          submitPrompt(prompt);
        }
      });
    });
  }

  // Reconnect SSE stream cleanly when a mobile browser tab wakes from sleep
  document.addEventListener('visibilitychange', function() {
    if (document.visibilityState === 'visible' && state.attendeeId && state.token) {
      if (!state.eventSource || state.eventSource.readyState === 2) {
        if (window.ActorView.tokenUsable(state.token, state.attendeeId)) {
          connectEventSource(true);
        }
      }
    }
  });

  // Cleanup on unload
  window.addEventListener('beforeunload', function() {
    if (state.eventSource) {
      state.eventSource.close();
    }
  });

  // Initialize
  window.addEventListener('DOMContentLoaded', function() {
    setupEvents();
    initSession();
  });
})();
