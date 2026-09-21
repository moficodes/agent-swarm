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
    qrCode: document.getElementById('qr-code'),
    qrLink: document.getElementById('qr-link'),
    qrUrlText: document.getElementById('qr-url-text'),
    parkingMeter: document.getElementById('parking-meter'),
    parkingCount: document.getElementById('parking-count'),
    parkingStateText: document.getElementById('parking-state-text'),
    parkingProgressBar: document.getElementById('parking-progress-bar'),
    parkingPulseDot: document.getElementById('parking-pulse-dot'),
    workerPod0: document.getElementById('worker-pod-0'),
    workerBadge0: document.getElementById('worker-badge-0'),
    workerActor0: document.getElementById('worker-actor-0'),
    workerTask0: document.getElementById('worker-task-0'),
    workerTimer0: document.getElementById('worker-timer-0'),
    workerDot0: document.getElementById('worker-dot-0'),
    workerPod1: document.getElementById('worker-pod-1'),
    workerBadge1: document.getElementById('worker-badge-1'),
    workerActor1: document.getElementById('worker-actor-1'),
    workerTask1: document.getElementById('worker-task-1'),
    workerTimer1: document.getElementById('worker-timer-1'),
    workerDot1: document.getElementById('worker-dot-1'),
    fleetGrid: document.getElementById('fleet-grid'),
    eventTicker: document.getElementById('event-ticker'),
    btnSim25: document.getElementById('btn-sim-25'),
    btnSim50: document.getElementById('btn-sim-50'),
    btnSimStop: document.getElementById('btn-sim-stop'),
    btnResetAll: document.getElementById('btn-reset-all'),
    spotlightDrawer: document.getElementById('spotlight-drawer'),
    spotlightBackdrop: document.getElementById('spotlight-backdrop'),
    spotlightTitle: document.getElementById('spotlight-title'),
    spotlightState: document.getElementById('spotlight-state'),
    spotlightClose: document.getElementById('spotlight-close'),
    spotlightWorker: document.getElementById('spotlight-worker'),
    spotlightSnapshot: document.getElementById('spotlight-snapshot'),
    spotlightFootprint: document.getElementById('spotlight-footprint'),
    spotlightPrompt: document.getElementById('spotlight-prompt'),
    spotlightCode: document.getElementById('spotlight-code'),
    spotlightOutput: document.getElementById('spotlight-output'),
    spotlightPhoneLink: document.getElementById('spotlight-phone-link'),
  };

  // State
  let lastEventSignature = '';
  let simulationActive = false;
  let simulationCount = 0;
  let currentSpotlightActorId = null;

  // -------------------------------------------------------------------------
  // Offline Pure-JS SVG QR Code Generator (ISO/IEC 18004 compliant subset)
  // Completely self-contained, zero external CDN / network dependencies.
  // -------------------------------------------------------------------------
  const GF256 = (function() {
    const exp = new Uint8Array(512);
    const log = new Uint8Array(256);
    let val = 1;
    for (let i = 0; i < 255; i++) {
      exp[i] = val;
      log[val] = i;
      val = (val << 1) ^ (val >= 128 ? 0x11d : 0);
    }
    for (let i = 255; i < 512; i++) {
      exp[i] = exp[i - 255];
    }
    return {
      mul(a, b) {
        if (a === 0 || b === 0) return 0;
        return exp[log[a] + log[b]];
      },
      polyMul(p1, p2) {
        const res = new Uint8Array(p1.length + p2.length - 1);
        for (let i = 0; i < p1.length; i++) {
          for (let j = 0; j < p2.length; j++) {
            res[i + j] ^= this.mul(p1[i], p2[j]);
          }
        }
        return res;
      },
      calcEC(data, ecCount) {
        let gen = new Uint8Array([1]);
        for (let i = 0; i < ecCount; i++) {
          gen = this.polyMul(gen, new Uint8Array([1, exp[i]]));
        }
        const msg = new Uint8Array(data.length + ecCount);
        msg.set(data);
        for (let i = 0; i < data.length; i++) {
          const coef = msg[i];
          if (coef !== 0) {
            for (let j = 0; j < gen.length; j++) {
              msg[i + j] ^= this.mul(coef, gen[j]);
            }
          }
        }
        return msg.slice(data.length);
      }
    };
  })();

  function generateQRCodeSVG(text) {
    // Versions specs: [version, size, dataCodewords, ecCodewords, alignmentCoords]
    const VERSIONS = [
      [1, 21, 19, 7, []],
      [2, 25, 34, 10, [6, 18]],
      [3, 29, 55, 15, [6, 22]],
      [4, 33, 80, 20, [6, 26]],
      [5, 37, 108, 26, [6, 30]],
      [6, 41, 134, 28, [6, 22, 38]],
    ];

    const enc = new TextEncoder();
    const rawBytes = enc.encode(text);
    const neededBytes = rawBytes.length + 3;

    let vSpec = VERSIONS.find(v => v[2] >= neededBytes);
    if (!vSpec) vSpec = VERSIONS[VERSIONS.length - 1];

    const [version, size, dataCapacity, ecCount, alignCoords] = vSpec;

    // Build bit stream: 4 bits mode (0100 for Byte) + 8 bits count + raw bytes
    const bits = [];
    function pushBits(val, len) {
      for (let i = len - 1; i >= 0; i--) {
        bits.push((val >> i) & 1);
      }
    }

    pushBits(0b0100, 4); // 8-bit byte mode
    pushBits(rawBytes.length, 8);
    for (let i = 0; i < rawBytes.length; i++) {
      pushBits(rawBytes[i], 8);
    }

    // Terminator (up to 4 zeroes)
    const totalBits = dataCapacity * 8;
    const termLen = Math.min(4, totalBits - bits.length);
    pushBits(0, termLen);

    // Byte boundary padding
    while (bits.length % 8 !== 0) {
      bits.push(0);
    }

    // Fill data codewords
    const dataBytes = new Uint8Array(dataCapacity);
    for (let i = 0; i < bits.length / 8; i++) {
      let byteVal = 0;
      for (let j = 0; j < 8; j++) {
        byteVal = (byteVal << 1) | bits[i * 8 + j];
      }
      dataBytes[i] = byteVal;
    }

    let padIdx = bits.length / 8;
    const padPatterns = [0xec, 0x11];
    let padToggle = 0;
    while (padIdx < dataCapacity) {
      dataBytes[padIdx++] = padPatterns[padToggle];
      padToggle ^= 1;
    }

    // Compute Error Correction codewords
    const ecBytes = GF256.calcEC(dataBytes, ecCount);

    const finalBits = [];
    function pushBytesToBits(arr, u8) {
      for (let i = 0; i < u8.length; i++) {
        for (let b = 7; b >= 0; b--) {
          arr.push((u8[i] >> b) & 1);
        }
      }
    }
    pushBytesToBits(finalBits, dataBytes);
    pushBytesToBits(finalBits, ecBytes);

    // Create Matrix
    const matrix = Array.from({ length: size }, () => Array(size).fill(null));
    const reserved = Array.from({ length: size }, () => Array(size).fill(false));

    function setModule(r, c, val, isRes = true) {
      if (r >= 0 && r < size && c >= 0 && c < size) {
        matrix[r][c] = val;
        if (isRes) reserved[r][c] = true;
      }
    }

    // Place Finder Patterns
    function placeFinder(top, left) {
      for (let r = -1; r <= 7; r++) {
        for (let c = -1; c <= 7; c++) {
          const nr = top + r;
          const nc = left + c;
          if (nr < 0 || nr >= size || nc < 0 || nc >= size) continue;
          if (r === -1 || r === 7 || c === -1 || c === 7) {
            setModule(nr, nc, 0);
          } else if (r === 0 || r === 6 || c === 0 || c === 6) {
            setModule(nr, nc, 1);
          } else if (r >= 2 && r <= 4 && c >= 2 && c <= 4) {
            setModule(nr, nc, 1);
          } else {
            setModule(nr, nc, 0);
          }
        }
      }
    }

    placeFinder(0, 0);
    placeFinder(0, size - 7);
    placeFinder(size - 7, 0);

    // Timing Patterns
    for (let i = 8; i < size - 8; i++) {
      const val = i % 2 === 0 ? 1 : 0;
      if (!reserved[6][i]) setModule(6, i, val);
      if (!reserved[i][6]) setModule(i, 6, val);
    }

    // Alignment Patterns
    if (alignCoords.length > 0) {
      for (let i = 0; i < alignCoords.length; i++) {
        for (let j = 0; j < alignCoords.length; j++) {
          const r = alignCoords[i];
          const c = alignCoords[j];
          if (reserved[r][c]) continue;
          for (let dr = -2; dr <= 2; dr++) {
            for (let dc = -2; dc <= 2; dc++) {
              const isBorder = Math.abs(dr) === 2 || Math.abs(dc) === 2;
              const isCenter = dr === 0 && dc === 0;
              setModule(r + dr, c + dc, (isBorder || isCenter) ? 1 : 0);
            }
          }
        }
      }
    }

    // Dark Module
    setModule(size - 8, 8, 1);

    // Reserve Format Info areas
    for (let i = 0; i < 9; i++) {
      reserved[8][i] = true;
      reserved[i][8] = true;
    }
    for (let i = size - 8; i < size; i++) {
      reserved[8][i] = true;
      reserved[i][8] = true;
    }

    // Place Data Bits with Mask 0 ((row + col) % 2 === 0)
    let bitIdx = 0;
    let dir = -1; // up
    let c = size - 1;
    while (c > 0) {
      if (c === 6) c--; // Skip vertical timing column
      const rStart = dir === -1 ? size - 1 : 0;
      const rEnd = dir === -1 ? -1 : size;
      for (let r = rStart; r !== rEnd; r += (dir === -1 ? -1 : 1)) {
        for (let dc = 0; dc < 2; dc++) {
          const col = c - dc;
          if (!reserved[r][col]) {
            let bit = bitIdx < finalBits.length ? finalBits[bitIdx++] : 0;
            if ((r + col) % 2 === 0) bit ^= 1; // Apply Mask 0
            matrix[r][col] = bit;
          }
        }
      }
      c -= 2;
      dir = -dir;
    }

    // Format Info bits for Level L (01), Mask 0 (000) = 0x77c4 (15 bits)
    const formatBits = 0x77c4;
    const fBits = [];
    for (let i = 14; i >= 0; i--) {
      fBits.push((formatBits >> i) & 1);
    }

    // Write format bits around top-left
    setModule(8, 0, fBits[0]);
    setModule(8, 1, fBits[1]);
    setModule(8, 2, fBits[2]);
    setModule(8, 3, fBits[3]);
    setModule(8, 4, fBits[4]);
    setModule(8, 5, fBits[5]);
    setModule(8, 7, fBits[6]);
    setModule(8, 8, fBits[7]);
    setModule(7, 8, fBits[8]);
    setModule(5, 8, fBits[9]);
    setModule(4, 8, fBits[10]);
    setModule(3, 8, fBits[11]);
    setModule(2, 8, fBits[12]);
    setModule(1, 8, fBits[13]);
    setModule(0, 8, fBits[14]);

    // Write format bits around top-right and bottom-left
    for (let i = 0; i < 7; i++) {
      setModule(size - 1 - i, 8, fBits[i]);
    }
    for (let i = 7; i < 15; i++) {
      setModule(8, size - 15 + i, fBits[i]);
    }

    // Convert matrix to SVG
    let path = '';
    for (let r = 0; r < size; r++) {
      for (let col = 0; col < size; col++) {
        if (matrix[r][col] === 1) {
          path += `M${col + 2},${r + 2}h1v1h-1z `;
        }
      }
    }

    const viewBoxSize = size + 4;
    return `<svg viewBox="0 0 ${viewBoxSize} ${viewBoxSize}" xmlns="http://www.w3.org/2000/svg" shape-rendering="crispEdges">
      <rect width="${viewBoxSize}" height="${viewBoxSize}" fill="#0a0e17" rx="1"/>
      <path d="${path.trim()}" fill="#00ffcc"/>
    </svg>`;
  }

  // -------------------------------------------------------------------------
  // UI Initialization
  // -------------------------------------------------------------------------
  function initOnboardingQR() {
    const chatUrl = window.location.origin + '/chat';
    if (el.qrUrlText) {
      el.qrUrlText.textContent = chatUrl;
    }
    if (el.qrLink) {
      el.qrLink.href = chatUrl;
    }
    if (el.qrCode) {
      try {
        const svg = generateQRCodeSVG(chatUrl);
        el.qrCode.innerHTML = svg;
        el.qrCode.addEventListener('click', function() {
          window.open(chatUrl, '_blank', 'noopener');
        });
      } catch (err) {
        console.error('Failed to generate offline QR code:', err);
        el.qrCode.innerHTML = '<div class="qr-fallback">Scan /chat on mobile</div>';
      }
    }
  }

  // -------------------------------------------------------------------------
  // Telemetry Updates
  // -------------------------------------------------------------------------
  let isTelemetryFetching = false;
  async function fetchTelemetry() {
    if (isTelemetryFetching) return;
    isTelemetryFetching = true;
    try {
      const resp = await fetch('/api/stage/telemetry', { cache: 'no-store' });
      if (!resp.ok) return;
      const data = await resp.json();
      updateDashboard(data);
      if (currentSpotlightActorId && el.spotlightDrawer && el.spotlightDrawer.classList.contains('open')) {
        refreshSpotlightDrawer(currentSpotlightActorId);
      }
    } catch (err) {
      // Background retry on transient network error
      console.warn('Telemetry poll transient error:', err);
    } finally {
      isTelemetryFetching = false;
    }
  }

  function updateDashboard(data) {
    if (!data) return;

    // 1. Update Envoy L7 Parking Meter
    updateParkingMeter(data.parked_requests ?? data.ParkedRequests ?? 0);

    // 2. Update Worker Pod Cards
    updateWorkerCards(data.workers || data.Workers || []);

    // 3. Update Attendee Fleet Badges
    renderFleetGrid(data.actors || data.Actors || {});

    // 4. Update Event Feed
    updateEventTicker(data.recent_events || data.RecentEvents || []);
  }

  function updateParkingMeter(parkedCount) {
    if (!el.parkingCount) return;
    el.parkingCount.textContent = parkedCount;

    // Compute progress gauge bar (0 to 50 scale)
    const maxScale = 50;
    const pct = Math.min(100, Math.max(0, Math.round((parkedCount / maxScale) * 100)));
    if (el.parkingProgressBar) {
      el.parkingProgressBar.style.width = pct + '%';
    }

    if (parkedCount > 0) {
      if (el.parkingMeter) el.parkingMeter.classList.add('active-parking');
      if (el.parkingPulseDot) el.parkingPulseDot.classList.add('pulsing');
      if (el.parkingStateText) {
        el.parkingStateText.textContent = 'ACTIVE PARKING';
        el.parkingStateText.className = 'parking-state state-burst';
      }
    } else {
      if (el.parkingMeter) el.parkingMeter.classList.remove('active-parking');
      if (el.parkingPulseDot) el.parkingPulseDot.classList.remove('pulsing');
      if (el.parkingStateText) {
        el.parkingStateText.textContent = 'IDLE';
        el.parkingStateText.className = 'parking-state';
      }
    }
  }

  function updateWorkerCards(workers) {
    // Pod 0
    if (workers.length > 0) {
      renderWorkerCard(
        workers[0],
        el.workerPod0,
        el.workerBadge0,
        el.workerActor0,
        el.workerTask0,
        el.workerTimer0,
        el.workerDot0
      );
    }
    // Pod 1
    if (workers.length > 1) {
      renderWorkerCard(
        workers[1],
        el.workerPod1,
        el.workerBadge1,
        el.workerActor1,
        el.workerTask1,
        el.workerTimer1,
        el.workerDot1
      );
    }
  }

  function renderWorkerCard(w, card, badge, actor, task, timer, dot) {
    if (!card || !w) return;
    const activeActor = w.active_actor || w.ActiveActor;
    const isBusy = activeActor && activeActor !== 'idle';
    const taskName = w.current_task || w.CurrentTask || (isBusy ? 'Running gVisor workload' : 'Waiting for actor dispatch');
    const elapsed = w.elapsed_ms ?? w.ElapsedMs ?? 0;

    if (isBusy) {
      card.classList.add('active');
      if (badge) {
        badge.textContent = 'ACTIVE';
        badge.className = 'worker-status-badge active';
      }
      if (dot) dot.className = 'worker-dot active';
      if (actor) {
        actor.textContent = formatActorName(activeActor);
        actor.className = 'stat-value active-actor-val running';
      }
      if (task) task.textContent = taskName;
      if (timer) timer.textContent = elapsed + ' ms';
    } else {
      card.classList.remove('active');
      if (badge) {
        badge.textContent = 'IDLE';
        badge.className = 'worker-status-badge idle';
      }
      if (dot) dot.className = 'worker-dot idle';
      if (actor) {
        actor.textContent = 'idle';
        actor.className = 'stat-value active-actor-val';
      }
      if (task) task.textContent = taskName;
      if (timer) timer.textContent = elapsed + ' ms';
    }
  }

  function formatActorName(id) {
    if (!id) return 'idle';
    const match = id.match(/attendee-(\d+)/i);
    if (match) {
      return 'Attendee ' + match[1].padStart(2, '0');
    }
    return id;
  }

  function renderFleetGrid(actorsMap) {
    const actors = actorsMap || {};
    // Iterate over 100 badge slots (Attendee 01 .. Attendee 100)
    for (let i = 1; i <= 100; i++) {
      const numStr = String(i).padStart(2, '0');
      const actorID = 'attendee-' + numStr;
      let badge = document.getElementById('badge-' + actorID);

      if (!badge && el.fleetGrid) {
        badge = document.createElement('div');
        badge.id = 'badge-' + actorID;
        badge.dataset.id = actorID;
        badge.className = 'fleet-badge suspended';
        badge.innerHTML = `<span class="badge-dot"></span><span class="badge-name">Attendee ${numStr}</span><span class="badge-state-label">SUSPENDED</span>`;
        el.fleetGrid.appendChild(badge);
      }
      if (!badge) continue;

      const rawState = (
        actors[actorID] ||
        actors[actorID.toLowerCase()] ||
        actors['attendee-' + i] ||
        'SUSPENDED'
      ).toUpperCase();

      let stateClass = 'suspended';
      let stateLabel = 'SUSPENDED';

      switch (rawState) {
        case 'PARKED':
          stateClass = 'parked';
          stateLabel = 'PARKED';
          break;
        case 'RUNNING':
          stateClass = 'running';
          stateLabel = 'RUNNING';
          break;
        case 'AWAITING_APPROVAL':
          stateClass = 'awaiting';
          stateLabel = 'AWAITING APPROVAL';
          break;
        default:
          stateClass = 'suspended';
          stateLabel = 'SUSPENDED';
          break;
      }

      badge.className = 'fleet-badge ' + stateClass;
      const labelEl = badge.querySelector('.badge-state-label');
      if (labelEl) {
        labelEl.textContent = stateLabel;
      }
    }
  }
  const updateFleetGrid = renderFleetGrid;

  function updateEventTicker(events) {
    if (!el.eventTicker) return;
    if (!events || events.length === 0) {
      return;
    }

    const sig = events.slice(-5).join('|');
    if (sig === lastEventSignature) {
      return; // No change
    }
    lastEventSignature = sig;

    // Show last 6 events reversed (most recent first)
    const recent = events.slice(-6).reverse();
    let html = '';
    for (let i = 0; i < recent.length; i++) {
      const text = escapeHTML(recent[i]);
      let icon = '⚡';
      if (text.includes('awaiting')) icon = '⏸️';
      else if (text.includes('parked') || text.includes('Parking')) icon = '⏳';
      else if (text.includes('executing') || text.includes('running')) icon = '🚀';
      else if (text.includes('error') || text.includes('Warning')) icon = '⚠️';
      else if (text.includes('completed') || text.includes('suspended')) icon = '💾';

      html += `<div class="ticker-item">
        <span class="ticker-icon" aria-hidden="true">${icon}</span>
        <span class="ticker-text">${text}</span>
      </div>`;
    }

    el.eventTicker.innerHTML = html;
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

  // -------------------------------------------------------------------------
  // Syntax Highlighter for Code in Spotlight Drawer
  // -------------------------------------------------------------------------
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

  // -------------------------------------------------------------------------
  // Spotlight Drawer Controller
  // -------------------------------------------------------------------------
  async function openSpotlightDrawer(actorId) {
    if (!actorId) return;
    currentSpotlightActorId = actorId;

    if (el.spotlightTitle) el.spotlightTitle.textContent = formatActorName(actorId);
    if (el.spotlightPhoneLink) el.spotlightPhoneLink.href = '/chat?attendee=' + encodeURIComponent(actorId);

    if (el.spotlightDrawer) el.spotlightDrawer.classList.add('open');
    if (el.spotlightBackdrop) el.spotlightBackdrop.classList.add('open');

    await refreshSpotlightDrawer(actorId);
  }

  async function refreshSpotlightDrawer(actorId) {
    if (!actorId) return;
    try {
      const resp = await fetch('/api/actor/' + encodeURIComponent(actorId), { cache: 'no-store' });
      if (!resp.ok) return;
      const data = await resp.json();
      if (currentSpotlightActorId === actorId) {
        populateSpotlightDrawer(data);
      }
    } catch (err) {
      console.warn('Error fetching actor details for spotlight:', err);
    }
  }

  function closeSpotlightDrawer() {
    currentSpotlightActorId = null;
    if (el.spotlightDrawer) el.spotlightDrawer.classList.remove('open');
    if (el.spotlightBackdrop) el.spotlightBackdrop.classList.remove('open');
  }

  function populateSpotlightDrawer(data) {
    if (!data) return;
    const actorId = data.actor_id || currentSpotlightActorId;
    if (el.spotlightTitle) {
      el.spotlightTitle.textContent = formatActorName(actorId);
    }

    // State badge
    const rawState = (data.state || 'SUSPENDED').toUpperCase();
    let stateClass = 'suspended';
    let stateLabel = rawState.replace(/_/g, ' ');

    if (rawState.includes('PARKED')) {
      stateClass = 'parked';
      stateLabel = 'PARKED (Envoy)';
    } else if (rawState.includes('RUNNING') || rawState.includes('THINKING')) {
      stateClass = 'running';
      stateLabel = rawState.includes('THINKING') ? 'THINKING (Vertex AI)' : 'RUNNING (gVisor)';
    } else if (rawState.includes('AWAITING')) {
      stateClass = 'awaiting';
      stateLabel = 'AWAITING APPROVAL';
    } else {
      stateClass = 'suspended';
      stateLabel = 'SUSPENDED (GCS)';
    }

    if (el.spotlightState) {
      el.spotlightState.textContent = stateLabel;
      el.spotlightState.className = 'drawer-state-badge ' + stateClass;
    }

    // Worker pod
    if (el.spotlightWorker) {
      if (data.worker_pod) {
        el.spotlightWorker.textContent = data.worker_pod;
        el.spotlightWorker.className = 'drawer-metric-value highlight-cyan';
      } else {
        el.spotlightWorker.textContent = 'None (Suspended to GCS)';
        el.spotlightWorker.className = 'drawer-metric-value';
      }
    }

    // Snapshot location
    if (el.spotlightSnapshot) {
      if (data.snapshot_location) {
        const sizeStr = data.snapshot_size_bytes ? ` (${Math.round(data.snapshot_size_bytes / 1024)} KB)` : '';
        el.spotlightSnapshot.textContent = data.snapshot_location + sizeStr;
        el.spotlightSnapshot.title = data.snapshot_location;
      } else {
        el.spotlightSnapshot.textContent = 'In-memory / No snapshot';
      }
    }

    // Footprint pill
    if (el.spotlightFootprint) {
      if (rawState.includes('RUNNING') || rawState.includes('THINKING')) {
        el.spotlightFootprint.textContent = '1 vCPU / 256 MB (Active Pod)';
        el.spotlightFootprint.className = 'drawer-metric-value highlight-cyan';
      } else if (rawState.includes('PARKED')) {
        el.spotlightFootprint.textContent = '0 vCPU / 0 MB (Held in Envoy)';
        el.spotlightFootprint.className = 'drawer-metric-value highlight-amber';
      } else {
        el.spotlightFootprint.textContent = '0 vCPU / 0 MB in GCS';
        el.spotlightFootprint.className = 'drawer-metric-value highlight-green';
      }
    }

    // Prompt block
    if (el.spotlightPrompt) {
      let promptText = '';
      if (data.pending_approval && data.pending_approval.summary) {
        promptText = data.pending_approval.summary;
      } else if (data.chat_history && data.chat_history.length > 0) {
        for (let i = data.chat_history.length - 1; i >= 0; i--) {
          if (data.chat_history[i].sender === 'user' && data.chat_history[i].content) {
            promptText = data.chat_history[i].content;
            break;
          }
        }
        if (!promptText && data.chat_history[data.chat_history.length - 1].content) {
          promptText = data.chat_history[data.chat_history.length - 1].content;
        }
      }
      el.spotlightPrompt.textContent = promptText || 'No active prompt.';
    }

    // Code block with syntax highlighting
    if (el.spotlightCode) {
      let codeStr = '';
      if (data.pending_approval && data.pending_approval.code) {
        codeStr = data.pending_approval.code;
      } else if (data.last_result && data.last_result.code) {
        codeStr = data.last_result.code;
      }
      if (codeStr) {
        el.spotlightCode.innerHTML = highlightCode(codeStr);
      } else {
        el.spotlightCode.textContent = '# No active code execution';
      }
    }

    // Terminal output console
    if (el.spotlightOutput) {
      if (data.last_result) {
        const res = data.last_result;
        let outputText = '';
        if (res.stdout) outputText += res.stdout;
        if (res.stderr) outputText += (outputText ? '\n[STDERR]\n' : '') + res.stderr;
        if (!outputText) outputText = '(execution returned 0 output)';
        const meta = `\n\n--- Process exited code ${res.exit_code ?? 0} (${res.duration_ms ?? 0} ms) ---`;
        el.spotlightOutput.textContent = outputText + meta;
      } else if (rawState === 'AWAITING_APPROVAL') {
        el.spotlightOutput.textContent = '[PAUSED] Execution paused waiting for operator approval in mobile view.';
      } else if (rawState.includes('RUNNING') || rawState.includes('THINKING')) {
        el.spotlightOutput.textContent = '[RUNNING] Executing inside gVisor sandbox container...';
      } else {
        el.spotlightOutput.textContent = 'Awaiting execution output...';
      }
    }

    // Phone link
    if (el.spotlightPhoneLink) {
      el.spotlightPhoneLink.href = '/chat?attendee=' + encodeURIComponent(actorId);
    }
  }

  // -------------------------------------------------------------------------
  // Audience Simulation & Reset Controls
  // -------------------------------------------------------------------------
  async function fetchSimulationStatus() {
    try {
      const resp = await fetch('/api/simulation', { cache: 'no-store' });
      if (!resp.ok) return;
      const data = await resp.json();
      simulationActive = !!data.running;
      simulationCount = data.count || 0;
      updateSimulationUI();
    } catch (err) {
      console.warn('Simulation status poll error:', err);
    }
  }

  function updateSimulationUI() {
    if (simulationActive) {
      if (el.btnSimStop) el.btnSimStop.style.display = 'inline-flex';
      if (el.btnSim25) {
        if (simulationCount === 25) {
          el.btnSim25.classList.add('active');
        } else {
          el.btnSim25.classList.remove('active');
        }
      }
      if (el.btnSim50) {
        if (simulationCount === 50) {
          el.btnSim50.classList.add('active');
        } else {
          el.btnSim50.classList.remove('active');
        }
      }
    } else {
      if (el.btnSimStop) el.btnSimStop.style.display = 'none';
      if (el.btnSim25) el.btnSim25.classList.remove('active');
      if (el.btnSim50) el.btnSim50.classList.remove('active');
    }
  }

  async function startSimulation(count) {
    try {
      if (count === 25 && el.btnSim25) el.btnSim25.classList.add('active');
      if (count === 50 && el.btnSim50) el.btnSim50.classList.add('active');
      const resp = await fetch('/api/simulation', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ action: 'start', count: count }),
      });
      if (resp.ok) {
        await fetchSimulationStatus();
        fetchTelemetry();
      }
    } catch (err) {
      console.error('Failed to start simulation:', err);
    }
  }

  async function stopSimulation() {
    try {
      const resp = await fetch('/api/simulation', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ action: 'stop' }),
      });
      if (resp.ok) {
        await fetchSimulationStatus();
        fetchTelemetry();
      }
    } catch (err) {
      console.error('Failed to stop simulation:', err);
    }
  }

  async function resetAllCluster() {
    if (!el.btnResetAll) return;
    const origText = el.btnResetAll.textContent;
    el.btnResetAll.textContent = '⟲ Resetting...';
    el.btnResetAll.disabled = true;

    try {
      const resp = await fetch('/api/reset', { method: 'POST' });
      if (resp.ok) {
        el.btnResetAll.textContent = '✓ Reset Complete';
        setTimeout(() => {
          if (el.btnResetAll) {
            el.btnResetAll.textContent = origText;
            el.btnResetAll.disabled = false;
          }
        }, 1500);
      } else {
        el.btnResetAll.textContent = '✕ Reset Failed';
        setTimeout(() => {
          if (el.btnResetAll) {
            el.btnResetAll.textContent = origText;
            el.btnResetAll.disabled = false;
          }
        }, 1500);
      }
      await fetchSimulationStatus();
      await fetchTelemetry();
      if (currentSpotlightActorId) {
        refreshSpotlightDrawer(currentSpotlightActorId);
      }
    } catch (err) {
      console.error('Failed to reset cluster:', err);
      el.btnResetAll.textContent = origText;
      el.btnResetAll.disabled = false;
    }
  }

  function initControls() {
    if (el.btnSim25) {
      el.btnSim25.addEventListener('click', function() {
        startSimulation(25);
      });
    }
    if (el.btnSim50) {
      el.btnSim50.addEventListener('click', function() {
        startSimulation(50);
      });
    }
    if (el.btnSimStop) {
      el.btnSimStop.addEventListener('click', function() {
        stopSimulation();
      });
    }
    if (el.btnResetAll) {
      el.btnResetAll.addEventListener('click', function() {
        resetAllCluster();
      });
    }

    if (el.spotlightClose) {
      el.spotlightClose.addEventListener('click', closeSpotlightDrawer);
    }
    if (el.spotlightBackdrop) {
      el.spotlightBackdrop.addEventListener('click', closeSpotlightDrawer);
    }
    document.addEventListener('keydown', function(e) {
      if (e.key === 'Escape' || e.key === 'Esc') {
        closeSpotlightDrawer();
      }
    });

    if (el.fleetGrid) {
      el.fleetGrid.addEventListener('click', function(e) {
        const badge = e.target.closest('.fleet-badge');
        if (badge) {
          const actorId = badge.dataset.id || badge.id.replace('badge-', '');
          if (actorId) {
            openSpotlightDrawer(actorId);
          }
        }
      });
    }
  }

  // -------------------------------------------------------------------------
  // Start
  // -------------------------------------------------------------------------
  document.addEventListener('DOMContentLoaded', function() {
    initOnboardingQR();
    initControls();
    fetchSimulationStatus();
    fetchTelemetry();
    setInterval(fetchTelemetry, 500);
    setInterval(fetchSimulationStatus, 2000);
  });
})();
