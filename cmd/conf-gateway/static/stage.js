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
  };

  // State
  let lastEventSignature = '';

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
  async function fetchTelemetry() {
    try {
      const resp = await fetch('/api/stage/telemetry', { cache: 'no-store' });
      if (!resp.ok) return;
      const data = await resp.json();
      updateDashboard(data);
    } catch (err) {
      // Background retry on transient network error
      console.warn('Telemetry poll transient error:', err);
    }
  }

  function updateDashboard(data) {
    if (!data) return;

    // 1. Update Envoy L7 Parking Meter
    updateParkingMeter(data.parked_requests || 0);

    // 2. Update Worker Pod Cards
    updateWorkerCards(data.workers || []);

    // 3. Update Attendee Fleet Badges
    updateFleetGrid(data.actors || {});

    // 4. Update Event Feed
    updateEventTicker(data.recent_events || []);
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
    if (!card) return;
    const isBusy = w.ActiveActor && w.ActiveActor !== 'idle';

    if (isBusy) {
      card.classList.add('active');
      if (badge) {
        badge.textContent = 'ACTIVE';
        badge.className = 'worker-status-badge active';
      }
      if (dot) dot.className = 'worker-dot active';
      if (actor) {
        actor.textContent = formatActorName(w.ActiveActor);
        actor.className = 'stat-value active-actor-val running';
      }
      if (task) task.textContent = w.CurrentTask || 'Running gVisor workload';
      if (timer) timer.textContent = (w.ElapsedMs || 0) + ' ms';
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
      if (task) task.textContent = 'Waiting for actor dispatch';
      if (timer) timer.textContent = '0 ms';
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

  function updateFleetGrid(actorsMap) {
    // Iterate over 50 badge slots
    for (let i = 1; i <= 50; i++) {
      const numStr = String(i).padStart(2, '0');
      const actorID = 'attendee-' + numStr;
      const badge = document.getElementById('badge-' + actorID);
      if (!badge) continue;

      const rawState = (actorsMap[actorID] || 'SUSPENDED').toUpperCase();
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
  // Start
  // -------------------------------------------------------------------------
  document.addEventListener('DOMContentLoaded', function() {
    initOnboardingQR();
    fetchTelemetry();
    setInterval(fetchTelemetry, 500);
  });
})();
