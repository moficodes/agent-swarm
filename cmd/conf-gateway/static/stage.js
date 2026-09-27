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
    mockBanner: document.getElementById('mock-banner'),
    workerPodsCurrent: document.getElementById('worker-pods-current'),
    workerPodsMax: document.getElementById('worker-pods-max'),
    workerPodsScaling: document.getElementById('worker-pods-scaling'),
    oversubRatio: document.getElementById('oversub-ratio'),
    oversubWaiting: document.getElementById('oversub-waiting'),
    qrCode: document.getElementById('qr-code'),
    qrLink: document.getElementById('qr-link'),
    qrUrlText: document.getElementById('qr-url-text'),
    parkingMeter: document.getElementById('parking-meter'),
    parkingCount: document.getElementById('parking-count'),
    parkingStateText: document.getElementById('parking-state-text'),
    parkingProgressBar: document.getElementById('parking-progress-bar'),
    parkingPulseDot: document.getElementById('parking-pulse-dot'),
    workerList: document.getElementById('worker-list'),
    workerPoolReplicas: document.getElementById('worker-pool-replicas'),
    fleetGrid: document.getElementById('fleet-grid'),
    fleetSub: document.getElementById('fleet-sub'),
    eventTicker: document.getElementById('event-ticker'),
    btnSim25: document.getElementById('btn-sim-25'),
    btnSim50: document.getElementById('btn-sim-50'),
    btnSimStop: document.getElementById('btn-sim-stop'),
    btnResetAll: document.getElementById('btn-reset-all'),
    spotlightDrawer: document.getElementById('spotlight-drawer'),
    spotlightBackdrop: document.getElementById('spotlight-backdrop'),
    spotlightTitle: document.getElementById('spotlight-title'),
    spotlightState: document.getElementById('spotlight-state'),
    spotlightActivity: document.getElementById('spotlight-activity'),
    spotlightClose: document.getElementById('spotlight-close'),
    spotlightWorker: document.getElementById('spotlight-worker'),
    spotlightSnapshot: document.getElementById('spotlight-snapshot'),
    spotlightFootprint: document.getElementById('spotlight-footprint'),
    spotlightPrompt: document.getElementById('spotlight-prompt'),
    spotlightCode: document.getElementById('spotlight-code'),
    spotlightOutput: document.getElementById('spotlight-output'),
    spotlightPhoneLink: document.getElementById('spotlight-phone-link'),
    viewModeGrid: document.getElementById('view-mode-grid'),
    viewModeStream: document.getElementById('view-mode-stream'),
    stageGridView: document.getElementById('stage-grid-view'),
    stageStreamView: document.getElementById('stage-stream-view'),
    stageActionStream: document.getElementById('stage-action-stream'),
    stageStreamSearch: document.getElementById('stage-stream-search'),
    stageStreamSearchClear: document.getElementById('stage-stream-search-clear'),
    stageStreamAutoscroll: document.getElementById('stage-stream-autoscroll'),
    streamFilterButtons: document.querySelectorAll('#stage-stream-view .btn-filter'),
  };

  // State
  let lastEventSignature = '';
  let simulationActive = false;
  let simulationCount = 0;
  let currentSpotlightActorId = null;
  let currentStreamFilter = 'all';
  let streamSearchQuery = '';
  const MAX_STREAM_CARDS = 100;
  let streamEventSource = null;
  let hasFetchedFeedHistory = false;

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
    updateParkingMeter(data.parked_requests || 0, data.parking_available);

    // 2. Update Worker Pod Cards
    updateWorkerCards(data.workers || []);

    // 3. Update Attendee Fleet Badges
    renderFleetGrid(data.actors || {});

    // 4. Update Event Feed
    updateEventTicker(data.recent_events || []);

    // 5. Update Mock LLM Banner
    updateMockBanner(!!data.mock_llm);

    // 6. Update the live pod count and oversubscription header
    updateWorkerPods(data.worker_pods, data.actor_count || 0, data.parked_requests || 0, data.parking_available);
  }

  // updateWorkerPods fills the header's pod count and ratio. Both show '--'
  // when the pool's size is unknown or zero, so the ratio never divides by 0.
  function updateWorkerPods(pods, actorCount, parkedCount, parkingAvailable) {
    if (!el.workerPodsCurrent) return;
    const p = pods || {};
    const ok = !!p.available && p.current > 0;

    el.workerPodsCurrent.textContent = ok ? String(p.current) : '--';
    el.workerPodsMax.textContent = ok && p.max > 0 ? 'of ' + p.max : '';
    el.oversubRatio.textContent = ok && actorCount > 0 ? Math.round(actorCount / p.current) + 'x' : '--';
    if (el.workerPoolReplicas) {
      el.workerPoolReplicas.textContent = (ok ? p.current + (p.current === 1 ? ' Replica' : ' Replicas') + ' // ' : '') +
        'Active Execution Seats';
    }

    const scaling = ok && p.desired > 0 && p.desired !== p.current;
    el.workerPodsScaling.hidden = !scaling;
    el.workerPodsScaling.textContent = scaling ? 'scaling ' + p.current + ' → ' + p.desired : '';

    const waiting = !!parkingAvailable && parkedCount > 0;
    el.oversubWaiting.hidden = !waiting;
    el.oversubWaiting.textContent = waiting ? parkedCount + ' waiting' : '';
  }

  function updateParkingMeter(parkedCount, parkingAvailable) {
    if (!el.parkingCount) return;

    if (!parkingAvailable) {
      el.parkingCount.textContent = '--';
      if (el.parkingProgressBar) el.parkingProgressBar.style.width = '0%';
      if (el.parkingMeter) el.parkingMeter.classList.remove('active-parking');
      if (el.parkingPulseDot) el.parkingPulseDot.classList.remove('pulsing');
      if (el.parkingStateText) {
        el.parkingStateText.textContent = 'Parking: unavailable';
        el.parkingStateText.className = 'parking-state';
      }
      return;
    }

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

  // updateMockBanner shows the "Mock LLM" banner when the gateway is
  // configured without a real Vertex AI backend.
  function updateMockBanner(mockLLM) {
    if (el.mockBanner) el.mockBanner.hidden = !mockLLM;
  }

  // renderWorkerCardHTML renders one real worker pod: its name, the actor it
  // is running (if any), and the actor's substrate state.
  function renderWorkerCardHTML(w, compact) {
    const activeActor = w.active_actor || '';
    const podName = w.pod_name || 'worker';
    // Compact cards keep only the pod name's random suffix, the part that
    // tells workers apart.
    const title = compact ? podName.split('-').pop() : podName;
    const isBusy = !!activeActor;
    const s = window.ActorView.state({ state: w.state });
    return `
      <div class="card worker-card${isBusy ? ' active' : ''}">
        <div class="worker-card-header">
          <div class="worker-id">
            <span class="worker-dot${isBusy ? ' active' : ''}"></span>
            <span class="worker-title" title="${escapeHTML(podName)}">${escapeHTML(title)}</span>
          </div>
          <span class="worker-status-badge ${s.cls}">${escapeHTML(s.label)}</span>
        </div>
        <div class="worker-body">
          <div class="worker-stat">
            <span class="stat-name">ACTIVE ACTOR</span>
            <span class="stat-value active-actor-val${isBusy ? ' running' : ''}">${escapeHTML(isBusy ? formatActorName(activeActor) : 'idle')}</span>
          </div>
        </div>
      </div>
    `;
  }

  function updateWorkerCards(workers) {
    if (!el.workerList) return;
    if (!workers || workers.length === 0) {
      el.workerList.innerHTML = '<p class="workers-empty">No actors running</p>';
      return;
    }
    // One row however many workers the pool has, so the fleet grid below
    // keeps its space; past two, cards switch to a compact layout.
    const compact = workers.length > 2;
    el.workerList.classList.toggle('compact', compact);
    el.workerList.style.gridTemplateColumns = 'repeat(' + Math.max(2, workers.length) + ', minmax(0, 1fr))';
    el.workerList.innerHTML = workers.map((w) => renderWorkerCardHTML(w, compact)).join('');
  }

  function formatActorName(id) {
    if (!id) return 'idle';
    const match = id.match(/attendee-(\d+)/i);
    if (match) {
      return 'Attendee ' + match[1].padStart(2, '0');
    }
    return id;
  }

  // renderFleetGrid draws one badge per actor the gateway reports, in ID
  // order, and drops badges for actors it no longer reports.
  function renderFleetGrid(actorsMap) {
    if (!el.fleetGrid) return;
    const actors = actorsMap || {};
    const ids = window.ActorView.fleetIDs(actors);
    if (el.fleetSub && ids.length > 0) {
      el.fleetSub.textContent = ids.length + ' actors multiplexed onto shared worker pods';
    }
    const keep = new Set(ids.map((id) => 'badge-' + id));
    Array.from(el.fleetGrid.children).forEach((child) => {
      if (!keep.has(child.id)) child.remove();
    });

    ids.forEach((actorID, i) => {
      let badge = document.getElementById('badge-' + actorID);
      if (!badge) {
        badge = document.createElement('div');
        badge.id = 'badge-' + actorID;
        badge.dataset.id = actorID;
        badge.innerHTML = '<span class="badge-dot"></span><span class="badge-name"></span>' +
          '<span class="badge-state-label"></span><span class="badge-state-sub"></span>';
        badge.querySelector('.badge-name').textContent = formatActorName(actorID);
      }
      if (el.fleetGrid.children[i] !== badge) {
        el.fleetGrid.insertBefore(badge, el.fleetGrid.children[i] || null);
      }

      const b = window.ActorView.badge(actors[actorID]);
      badge.className = 'fleet-badge ' + b.cls;
      badge.querySelector('.badge-state-label').textContent = b.label;
      badge.querySelector('.badge-state-sub').textContent = b.sub;
    });
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
  // Syntax Highlighter for Code in Spotlight Drawer & Stream Cards
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
        out += escapeHTML(code.slice(lastIndex, matchIndex));
      }
      const token = match[0];
      if (token.startsWith('#')) {
        out += `<span class="syn-com tok-comment">${escapeHTML(token)}</span>`;
      } else if (token.startsWith('"') || token.startsWith("'")) {
        out += `<span class="syn-str tok-string">${escapeHTML(token)}</span>`;
      } else if (/^\d/.test(token)) {
        out += `<span class="syn-num tok-number">${escapeHTML(token)}</span>`;
      } else {
        out += `<span class="syn-kwd tok-keyword">${escapeHTML(token)}</span>`;
      }
      lastIndex = tokenRegex.lastIndex;
    }

    if (lastIndex < code.length) {
      out += escapeHTML(code.slice(lastIndex));
    }
    return out;
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

    // State badge and activity chip
    const s = window.ActorView.state(data);
    const a = window.ActorView.activity(data);

    if (el.spotlightState) {
      el.spotlightState.textContent = s.label;
      el.spotlightState.className = 'drawer-state-badge ' + s.cls;
    }
    if (el.spotlightActivity) {
      el.spotlightActivity.hidden = !a;
      if (a) {
        el.spotlightActivity.className = 'activity-chip ' + a.cls;
        el.spotlightActivity.textContent = a.label;
      }
    }

    // Worker pod
    if (el.spotlightWorker) {
      el.spotlightWorker.textContent = data.worker_pod || 'None';
      el.spotlightWorker.className = 'drawer-metric-value' + (data.worker_pod ? ' highlight-cyan' : '');
    }

    // Snapshot
    if (el.spotlightSnapshot) {
      if (data.snapshot_uri) {
        el.spotlightSnapshot.textContent = data.snapshot_uri;
        el.spotlightSnapshot.title = data.snapshot_uri;
      } else {
        el.spotlightSnapshot.textContent = 'No snapshot';
        el.spotlightSnapshot.removeAttribute('title');
      }
    }

    // Model usage: calls, prompt/candidate tokens, and last call latency.
    if (el.spotlightFootprint) {
      const usage = data.usage || {};
      el.spotlightFootprint.textContent =
        (usage.calls || 0) + ' calls · ' +
        (usage.prompt_tokens || 0) + '/' + (usage.candidate_tokens || 0) + ' tokens · ' +
        (usage.last_latency_ms || 0) + ' ms last call';
      el.spotlightFootprint.className = 'drawer-metric-value';
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

    // Terminal output console. A failed run shows one line in amber; a
    // corrected one never reaches last_result.
    if (el.spotlightOutput) {
      const failure = data.last_result ? window.ActorView.run(data.last_result) : null;
      el.spotlightOutput.classList.toggle('run-failed', !!failure);
      if (failure) {
        el.spotlightOutput.textContent = failure.label.toUpperCase() + (failure.detail ? '\n' + failure.detail : '');
      } else if (data.last_result) {
        const res = data.last_result;
        let outputText = '';
        if (res.stdout) outputText += res.stdout;
        if (res.stderr) outputText += (outputText ? '\n[STDERR]\n' : '') + res.stderr;
        if (!outputText) outputText = '(execution returned 0 output)';
        const meta = `\n\n--- Process exited code ${res.exit_code ?? 0} (${res.duration_ms ?? 0} ms) ---`;
        el.spotlightOutput.textContent = outputText + meta;
      } else if (a) {
        el.spotlightOutput.textContent = '[' + a.label.toUpperCase() + '] Waiting on the actor to finish...';
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
      handleStreamReset();
      if (currentSpotlightActorId) {
        refreshSpotlightDrawer(currentSpotlightActorId);
      }
    } catch (err) {
      console.error('Failed to reset cluster:', err);
      el.btnResetAll.textContent = origText;
      el.btnResetAll.disabled = false;
    }
  }

  // -------------------------------------------------------------------------
  // Integrated Stream View Controller
  // -------------------------------------------------------------------------
  function switchViewMode(mode) {
    if (mode === 'grid') {
      if (el.viewModeGrid) {
        el.viewModeGrid.classList.add('active');
        el.viewModeGrid.setAttribute('aria-pressed', 'true');
      }
      if (el.viewModeStream) {
        el.viewModeStream.classList.remove('active');
        el.viewModeStream.setAttribute('aria-pressed', 'false');
      }
      if (el.stageGridView) {
        el.stageGridView.style.display = '';
        el.stageGridView.classList.add('active');
      }
      if (el.stageStreamView) {
        el.stageStreamView.style.display = 'none';
        el.stageStreamView.classList.remove('active');
      }
    } else if (mode === 'stream') {
      if (el.viewModeStream) {
        el.viewModeStream.classList.add('active');
        el.viewModeStream.setAttribute('aria-pressed', 'true');
      }
      if (el.viewModeGrid) {
        el.viewModeGrid.classList.remove('active');
        el.viewModeGrid.setAttribute('aria-pressed', 'false');
      }
      if (el.stageStreamView) {
        el.stageStreamView.style.display = 'flex';
        el.stageStreamView.classList.add('active');
      }
      if (el.stageGridView) {
        el.stageGridView.style.display = 'none';
        el.stageGridView.classList.remove('active');
      }
      if (!hasFetchedFeedHistory) {
        fetchStreamFeedHistory();
      }
    }
  }

  function formatStreamTimestamp(ts) {
    if (!ts) return 'Just now';
    const date = new Date(ts * 1000);
    return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }

  // getStreamActivityHTML mirrors feed.js's getActivityHTML: it renders what
  // the gateway actually observed for this entry, not an invented state.
  function getStreamActivityHTML(entry) {
    const a = window.ActorView.activity({ activity: entry.activity });
    const src = window.ActorView.source(entry.source);
    let html = a ? `<span class="status-pill ${a.cls}">${escapeHTML(a.label)}</span>` : '';
    if (src) html += `<span class="source-tag ${src.cls}">${escapeHTML(src.label)}</span>`;
    if (entry.worker_pod) {
      html += `<span class="footprint-pill" title="Worker pod">${escapeHTML(entry.worker_pod)}</span>`;
    }
    return html;
  }

  function renderStreamCardInner(entry) {
    const attendeeId = entry.attendee_id || 'unknown';
    const attendeeDisplayName = formatActorName(attendeeId);

    // Risk Pill
    let riskHTML = '';
    if (entry.risk_level) {
      const rk = ['low', 'medium', 'high'].includes(entry.risk_level.toLowerCase()) ? entry.risk_level.toLowerCase() : 'high';
      riskHTML = `<span class="risk-pill ${rk}">${escapeHTML(entry.risk_level)} RISK</span>`;
    }

    // Prompt Section
    let promptHTML = '';
    if (entry.prompt) {
      promptHTML = `
        <div class="card-prompt-section">
          <span class="section-label">User Prompt</span>
          <div class="card-prompt-text">${escapeHTML(entry.prompt)}</div>
        </div>
      `;
    }

    // Model Response Section
    let responseHTML = '';
    if (entry.model_response && !entry.model_response.startsWith('Execution completed in ')) {
      responseHTML = `
        <div class="card-response-section">
          <span class="section-label">AI Reasoning &amp; Plan</span>
          <div class="card-response-text">${escapeHTML(entry.model_response)}</div>
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
            <span class="code-lang-tag">${escapeHTML(lang)}</span>
            <div class="code-actions">
              <button type="button" class="btn-copy-code" title="Copy code snippet">
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
              <span class="term-exit-status ${failure.cls}">${escapeHTML(failure.label)}</span>
            </div>
          </div>
          <div class="terminal-body">
            ${failure.detail ? `<div class="term-detail">${escapeHTML(failure.detail)}</div>` : ''}
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
            ${entry.execution.stdout ? `<div class="term-stdout">${escapeHTML(entry.execution.stdout)}</div>` : ''}
            ${entry.execution.stderr ? `<div class="term-stderr">${escapeHTML(entry.execution.stderr)}</div>` : ''}
          </div>
        </div>
      `;
    }

    return `
      <div class="card-header-row">
        <div class="card-attendee-meta">
          <a href="/chat?attendee=${encodeURIComponent(attendeeId)}" class="attendee-link" title="Click to view in Spotlight Drawer">
            <span class="attendee-glyph" aria-hidden="true">👤</span> ${escapeHTML(attendeeDisplayName)}
          </a>
          <span class="card-timestamp">${formatStreamTimestamp(entry.timestamp)}</span>
        </div>
        <div class="card-badges">
          ${getStreamActivityHTML(entry)}
          ${riskHTML}
        </div>
      </div>

      ${promptHTML}
      ${responseHTML}
      ${codeHTML}
      ${terminalHTML}

      <div class="card-footer-row">
        <div class="footer-left">
          <span>Action ID: <code>${escapeHTML(entry.id)}</code></span>
        </div>
        <div class="footer-right">
          <span>Target: <code>ate-demo-conf/${escapeHTML(attendeeId)}</code></span>
        </div>
      </div>
    `;
  }

  function renderOrUpdateStreamCard(entry, skipScroll) {
    if (!el.stageActionStream || !entry || !entry.id) return;

    // Clear empty state placeholder if present
    const emptyState = document.getElementById('stage-stream-empty');
    if (emptyState) {
      emptyState.remove();
    }

    // Clear clean slate banner if present
    const cleanSlate = el.stageActionStream.querySelector('.clean-slate-banner');
    if (cleanSlate) {
      cleanSlate.remove();
    }

    const cardId = 'stream-action-' + entry.id;
    let card = document.getElementById(cardId);

    if (card) {
      // In-place update
      card.dataset.activity = entry.activity || '';
      card.dataset.attendee = entry.attendee_id || '';
      card.innerHTML = renderStreamCardInner(entry);
    } else {
      // Create new card and prepend to top
      card = document.createElement('article');
      card.className = 'action-card';
      card.id = cardId;
      card.dataset.id = entry.id;
      card.dataset.activity = entry.activity || '';
      card.dataset.attendee = entry.attendee_id || '';
      card.innerHTML = renderStreamCardInner(entry);

      el.stageActionStream.prepend(card);

      // Memory Bounding: maintain max MAX_STREAM_CARDS in DOM
      while (el.stageActionStream.children.length > MAX_STREAM_CARDS) {
        el.stageActionStream.removeChild(el.stageActionStream.lastElementChild);
      }

      // Auto-scroll to top when enabled
      if (!skipScroll && el.stageStreamAutoscroll && el.stageStreamAutoscroll.checked) {
        el.stageActionStream.scrollTo({ top: 0, behavior: 'smooth' });
      }
    }

    // Apply active filter and search visibility to this card
    updateStreamCardVisibility(card);
  }

  function updateStreamCardVisibility(card) {
    const activity = (card.dataset.activity || '').toUpperCase();
    const hasExecution = !!card.querySelector('.card-terminal-section');

    let stateMatch = false;
    if (currentStreamFilter === 'all') {
      stateMatch = true;
    } else if (currentStreamFilter === 'thinking') {
      stateMatch = (activity === 'THINKING');
    } else if (currentStreamFilter === 'awaiting') {
      stateMatch = (activity === 'AWAITING_APPROVAL');
    } else if (currentStreamFilter === 'executed') {
      stateMatch = hasExecution;
    }

    let searchMatch = true;
    if (streamSearchQuery) {
      const cardText = card.textContent.toLowerCase();
      searchMatch = cardText.includes(streamSearchQuery);
    }

    if (stateMatch && searchMatch) {
      card.style.display = '';
    } else {
      card.style.display = 'none';
    }
  }

  function applyAllStreamFilters() {
    if (!el.stageActionStream) return;
    const cards = el.stageActionStream.querySelectorAll('.action-card');
    cards.forEach(card => updateStreamCardVisibility(card));
  }

  function handleStreamReset() {
    if (!el.stageActionStream) return;
    el.stageActionStream.innerHTML = `
      <div class="clean-slate-banner">
        <div class="banner-icon" aria-hidden="true">⟲</div>
        <div class="banner-body">
          <h4 class="banner-title">Cluster Reset Complete</h4>
          <p class="banner-desc">All actors reset. Stream cleared.</p>
        </div>
      </div>
    `;
  }

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

  function connectStageStream() {
    if (streamEventSource) return;
    streamEventSource = new EventSource('/api/stream');

    streamEventSource.addEventListener('action_entry', (e) => {
      try {
        const entry = JSON.parse(e.data);
        renderOrUpdateStreamCard(entry);
      } catch (err) {
        console.error('Failed to parse action_entry JSON:', err);
      }
    });

    streamEventSource.addEventListener('reset', () => {
      handleStreamReset();
    });

    streamEventSource.onerror = (err) => {
      console.warn('Stage stream SSE connection interrupted, retrying...', err);
    };
  }

  async function fetchStreamFeedHistory() {
    hasFetchedFeedHistory = true;
    try {
      const resp = await fetch('/api/feed/history', { cache: 'no-store' });
      if (!resp.ok) return;
      const history = await resp.json();
      if (Array.isArray(history) && history.length > 0) {
        history.forEach(entry => renderOrUpdateStreamCard(entry, true));
      }
    } catch (err) {
      console.error('Failed to fetch stream feed history:', err);
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

    // View Switcher Handlers
    if (el.viewModeGrid) {
      el.viewModeGrid.addEventListener('click', function() {
        switchViewMode('grid');
      });
    }
    if (el.viewModeStream) {
      el.viewModeStream.addEventListener('click', function() {
        switchViewMode('stream');
      });
    }

    // Stream Filter Buttons
    if (el.streamFilterButtons) {
      el.streamFilterButtons.forEach(btn => {
        btn.addEventListener('click', function() {
          el.streamFilterButtons.forEach(b => {
            b.classList.remove('active');
            b.setAttribute('aria-pressed', 'false');
          });
          btn.classList.add('active');
          btn.setAttribute('aria-pressed', 'true');
          currentStreamFilter = btn.dataset.filter || 'all';
          applyAllStreamFilters();
        });
      });
    }

    // Stream Search Input
    if (el.stageStreamSearch) {
      el.stageStreamSearch.addEventListener('input', function() {
        streamSearchQuery = el.stageStreamSearch.value.trim().toLowerCase();
        if (el.stageStreamSearchClear) {
          el.stageStreamSearchClear.style.display = streamSearchQuery ? 'inline-block' : 'none';
        }
        applyAllStreamFilters();
      });
    }

    if (el.stageStreamSearchClear) {
      el.stageStreamSearchClear.addEventListener('click', function() {
        if (el.stageStreamSearch) {
          el.stageStreamSearch.value = '';
          streamSearchQuery = '';
          el.stageStreamSearchClear.style.display = 'none';
          el.stageStreamSearch.focus();
          applyAllStreamFilters();
        }
      });
    }

    // Stream Action Stream Click Delegation:
    // Copy code button copies snippet; clicking card opens Spotlight Drawer!
    if (el.stageActionStream) {
      el.stageActionStream.addEventListener('click', function(e) {
        const copyBtn = e.target.closest('.btn-copy-code');
        if (copyBtn) {
          e.stopPropagation();
          const section = copyBtn.closest('.card-code-section');
          if (section) {
            const codeEl = section.querySelector('code');
            if (codeEl) {
              copyTextToClipboard(codeEl.textContent, copyBtn);
            }
          }
          return;
        }

        const card = e.target.closest('.action-card');
        if (card) {
          const actorId = card.dataset.attendee;
          if (actorId) {
            if (e.target.closest('a')) {
              e.preventDefault();
            }
            openSpotlightDrawer(actorId);
          }
        }
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
  // Start & Lifecycle Cleanup
  // -------------------------------------------------------------------------
  window.addEventListener('beforeunload', function() {
    if (streamEventSource) {
      streamEventSource.close();
    }
  });

  document.addEventListener('DOMContentLoaded', function() {
    initOnboardingQR();
    initControls();
    fetchSimulationStatus();
    fetchTelemetry();
    connectStageStream();
    fetchStreamFeedHistory();
    setInterval(fetchTelemetry, 500);
    setInterval(fetchSimulationStatus, 2000);
  });
})();
