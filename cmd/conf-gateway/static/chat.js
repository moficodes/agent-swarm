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
    parkedTimer: null,
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
    approvalTemplate: document.getElementById('approval-card-template'),
  };

  // Lifecycle Pill States
  const LIFECYCLE_STATES = {
    SUSPENDED: {
      className: 'suspended',
      text: '⚡ Suspended in GCS',
      sub: '0 vCPU / 0 MB RAM',
    },
    PARKED: {
      className: 'parked',
      text: '⏳ Parked in Envoy',
      sub: 'L7 Request Buffer',
    },
    RUNNING: {
      className: 'running',
      text: '🚀 Running on Pod',
      sub: 'gVisor runsc sandbox',
    },
    AWAITING: {
      className: 'awaiting',
      text: '⏸️ Awaiting Approval',
      sub: 'Process frozen to GCS',
    },
  };

  function setStatusPill(pillState) {
    if (!el.statusPill) return;
    el.statusPill.className = 'status-pill ' + pillState.className;
    if (el.statusText) el.statusText.textContent = pillState.text;
    if (el.statusSub) el.statusSub.textContent = pillState.sub;
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

  // Lightweight Offline Syntax Highlighter for Python / Shell
  function highlightCode(rawCode) {
    if (!rawCode) return '';
    const escaped = escapeHTML(rawCode);

    // Regex tokens
    return escaped
      // Comments
      .replace(/(#[^\n]*)/g, '<span class="syn-com">$1</span>')
      // Strings double and single quotes
      .replace(/(&quot;[^&]*&quot;|&#39;[^&]*&#39;)/g, '<span class="syn-str">$1</span>')
      // Numbers
      .replace(/\b(\d+)\b/g, '<span class="syn-num">$1</span>')
      // Keywords
      .replace(/\b(def|class|import|from|return|if|elif|else|for|while|try|except|finally|with|as|pass|break|continue|lambda|yield|in|is|not|and|or|True|False|None)\b/g, '<span class="syn-kwd">$1</span>')
      // Common builtins
      .replace(/\b(print|len|range|str|int|float|list|dict|set|open|exec|sys|os|time|json)\b/g, '<span class="syn-fn">$1</span>');
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
  async function initSession() {
    const storedId = localStorage.getItem('conf_attendee_id');
    const storedToken = localStorage.getItem('conf_attendee_token');

    // If query string has reset, clear stored session
    const urlParams = new URLSearchParams(window.location.search);
    const forceReset = urlParams.get('reset') === '1';

    if (!forceReset && storedId && storedToken) {
      state.attendeeId = storedId;
      state.token = storedToken;
      if (el.attendeeBadge) el.attendeeBadge.textContent = formatAttendeeBadge(state.attendeeId);
      setStatusPill(LIFECYCLE_STATES.SUSPENDED);
      return;
    }

    try {
      const resp = await fetch('/api/session', { method: 'GET' });
      if (!resp.ok) {
        throw new Error('HTTP ' + resp.status + ' initializing session');
      }
      const data = await resp.json();
      state.attendeeId = data.attendee_id;
      state.token = data.token;
      localStorage.setItem('conf_attendee_id', state.attendeeId);
      localStorage.setItem('conf_attendee_token', state.token);

      if (el.attendeeBadge) el.attendeeBadge.textContent = formatAttendeeBadge(state.attendeeId);
      setStatusPill(LIFECYCLE_STATES.SUSPENDED);
    } catch (err) {
      console.error('Failed to init session:', err);
      appendSystemMessage('Unable to connect to gateway session. Please refresh the page.');
    }
  }

  // Feed Rendering
  function appendMessage(sender, content, timestamp) {
    const msgDiv = document.createElement('div');
    msgDiv.className = 'chat-message ' + (sender === 'user' ? 'user-message' : 'assistant-message');

    const metaDiv = document.createElement('div');
    metaDiv.className = 'message-meta';

    const senderSpan = document.createElement('span');
    senderSpan.className = 'sender-label';
    senderSpan.textContent = sender === 'user' ? formatAttendeeBadge(state.attendeeId) : 'Gemini Agent';

    const timeSpan = document.createElement('span');
    timeSpan.className = 'timestamp';
    timeSpan.textContent = formatTime(timestamp || Date.now());

    metaDiv.appendChild(senderSpan);
    metaDiv.appendChild(timeSpan);

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

  // Chat Submission
  async function submitPrompt(text) {
    if (!text || state.inFlight) return;
    state.inFlight = true;
    el.sendBtn.disabled = true;
    el.promptInput.disabled = true;

    appendMessage('user', text);
    el.promptInput.value = '';

    // Status: initially Running on Pod
    setStatusPill(LIFECYCLE_STATES.RUNNING);

    // If request takes more than 400ms, indicate it might be parked in Envoy
    clearTimeout(state.parkedTimer);
    state.parkedTimer = setTimeout(function() {
      if (state.inFlight) {
        setStatusPill(LIFECYCLE_STATES.PARKED);
      }
    }, 400);

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

      clearTimeout(state.parkedTimer);
      removeTypingIndicator();

      if (resp.status === 401) {
        // Token expired or invalid: refresh session
        localStorage.removeItem('conf_attendee_token');
        await initSession();
        appendSystemMessage('⚠️ Session was refreshed. Please re-submit your prompt.');
        setStatusPill(LIFECYCLE_STATES.SUSPENDED);
        return;
      }

      if (!resp.ok) {
        const errorText = await resp.text();
        appendSystemMessage('❌ Error from agent gateway: ' + (errorText || resp.statusText));
        setStatusPill(LIFECYCLE_STATES.SUSPENDED);
        return;
      }

      const chatMsg = await resp.json();
      const msgDiv = appendMessage('assistant', chatMsg.content, chatMsg.timestamp);

      if (chatMsg.approval) {
        // Paused for human approval: 0 MB RAM, state is Awaiting / Suspended
        setStatusPill(LIFECYCLE_STATES.AWAITING);
        appendApprovalCard(chatMsg.approval, msgDiv);
      } else {
        // Completed turn: suspended back to GCS
        setStatusPill(LIFECYCLE_STATES.SUSPENDED);
      }
    } catch (err) {
      console.error('Chat error:', err);
      clearTimeout(state.parkedTimer);
      removeTypingIndicator();
      appendSystemMessage('❌ Network communication error: ' + err.message);
      setStatusPill(LIFECYCLE_STATES.SUSPENDED);
    } finally {
      state.inFlight = false;
      el.sendBtn.disabled = false;
      el.promptInput.disabled = false;
      el.promptInput.focus();
    }
  }

  // Approval Decision Handling
  async function handleApprovalDecision(approvalId, decision, cardElement) {
    if (!approvalId) return;

    const btnReject = cardElement.querySelector('.btn-reject');
    const btnApprove = cardElement.querySelector('.btn-approve');
    if (btnReject) btnReject.disabled = true;
    if (btnApprove) btnApprove.disabled = true;

    setStatusPill(LIFECYCLE_STATES.RUNNING);

    // If wait > 400ms, mark as parked in Envoy queue
    clearTimeout(state.parkedTimer);
    state.parkedTimer = setTimeout(function() {
      setStatusPill(LIFECYCLE_STATES.PARKED);
    }, 400);

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

      clearTimeout(state.parkedTimer);
      removeTypingIndicator();

      if (!resp.ok) {
        const errorText = await resp.text();
        appendSystemMessage('❌ Failed to submit approval: ' + (errorText || resp.statusText));
        if (btnReject) btnReject.disabled = false;
        if (btnApprove) btnApprove.disabled = false;
        setStatusPill(LIFECYCLE_STATES.SUSPENDED);
        return;
      }

      // Replace actions with finalized badge
      const actionsDiv = cardElement.querySelector('.approval-actions');
      if (actionsDiv) {
        actionsDiv.innerHTML = '<div class="approval-status-badge ' + (decision === 'APPROVE' ? 'approved' : 'rejected') + '">' +
          (decision === 'APPROVE' ? '✓ Execution Approved &amp; Executed' : '✕ Execution Rejected') +
          '</div>';
      }

      const resultMsg = await resp.json();
      appendMessage('assistant', resultMsg.content, resultMsg.timestamp);
      setStatusPill(LIFECYCLE_STATES.SUSPENDED);
    } catch (err) {
      console.error('Approval submission error:', err);
      clearTimeout(state.parkedTimer);
      removeTypingIndicator();
      appendSystemMessage('❌ Error submitting decision: ' + err.message);
      if (btnReject) btnReject.disabled = false;
      if (btnApprove) btnApprove.disabled = false;
      setStatusPill(LIFECYCLE_STATES.SUSPENDED);
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
        const prompt = chip.getAttribute('data-prompt');
        if (prompt) {
          submitPrompt(prompt);
        }
      });
    });
  }

  // Initialize
  window.addEventListener('DOMContentLoaded', function() {
    setupEvents();
    initSession();
  });
})();
