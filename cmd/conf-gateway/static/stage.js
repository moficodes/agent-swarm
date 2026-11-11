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
    workspaceMode: document.getElementById('workspace-mode'),
    workspaceBound: document.getElementById('workspace-bound'),
    githubRepoLink: document.getElementById('github-repo-link'),
    githubQrCode: document.getElementById('github-qr-code'),
    qrCode: document.getElementById('qr-code'),
    qrLink: document.getElementById('qr-link'),
    qrUrlText: document.getElementById('qr-url-text'),
    parkingMeter: document.getElementById('parking-meter'),
    parkingCount: document.getElementById('parking-count'),
    parkingStateText: document.getElementById('parking-state-text'),
    parkingProgressBar: document.getElementById('parking-progress-bar'),
    parkingPulseDot: document.getElementById('parking-pulse-dot'),
    workerList: document.getElementById('worker-list'),
    workerTopoNav: document.getElementById('worker-topo-nav'),
    workerPoolReplicas: document.getElementById('worker-pool-replicas'),
    workerPodsSeats: document.getElementById('worker-pods-seats'),
    fleetGrid: document.getElementById('fleet-grid'),
    fleetCanvasWrap: document.getElementById('fleet-canvas-wrap'),
    fleetCanvas: document.getElementById('fleet-canvas'),
    fleetSub: document.getElementById('fleet-sub'),
    fleetZoomToolbar: document.getElementById('fleet-zoom-toolbar'),
    fleetZoomPresets: document.querySelectorAll('.btn-fleet-zoom'),
    btnFleetZoomOut: document.getElementById('btn-fleet-zoom-out'),
    btnFleetZoomIn: document.getElementById('btn-fleet-zoom-in'),
    fleetZoomReadout: document.getElementById('fleet-zoom-readout'),
    btnFleetZoomReset: document.getElementById('btn-fleet-zoom-reset'),
    btnFleetLoupe: document.getElementById('btn-fleet-loupe'),
    fleetJumpInput: document.getElementById('fleet-jump-input'),
    btnFleetJump: document.getElementById('btn-fleet-jump'),
    fleetMinimapWrap: document.getElementById('fleet-minimap-wrap'),
    fleetMinimap: document.getElementById('fleet-minimap'),
    minimapCoords: document.getElementById('minimap-coords'),
    btnMinimapReset: document.getElementById('btn-minimap-reset'),
    hoverTooltip: document.getElementById('stage-hover-tooltip'),
    hoverTipDot: document.getElementById('hover-tip-dot'),
    hoverTipTitle: document.getElementById('hover-tip-title'),
    hoverTipTag: document.getElementById('hover-tip-tag'),
    hoverTipState: document.getElementById('hover-tip-state'),
    hoverTipMeta: document.getElementById('hover-tip-meta'),
    hoverTipList: document.getElementById('hover-tip-list'),
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
    spotlightVolume: document.getElementById('spotlight-volume'),
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
  let simTargetLow = 25;
  let simTargetHigh = 50;
  let latestActorsMap = {};
  let latestFleetCompact = '';
  let latestActorCount = 50;
  let latestSeatsPerWorker = 4;
  let latestSeatsTotal = 0;
  let latestRunningActors = 0;
  let latestSuspendedActors = 0;
  let podHoverDirectory = {};
  let seatedActorPodMap = {};
  let latestNodeGroups = [];
  let latestWorkerList = [];
  let latestWorkerSeats = 4;
  let latestWorkerPodsMeta = null;
  let topoSelectedNode = null;
  let topoSelectedPod = null;
  let topoHighlightPod = null;
  let canvasCols = 512;
  let canvasRows = 128;
  let fleetZoom = 1;
  let fleetPanX = 0;
  let fleetPanY = 0;
  let fleetLoupeEnabled = true;
  let fleetIsDragging = false;
  let fleetDragMoved = false;
  let fleetDragStartX = 0;
  let fleetDragStartY = 0;
  let fleetDragStartPanX = 0;
  let fleetDragStartPanY = 0;
  let lastCanvasMouseX = -1;
  let lastCanvasMouseY = -1;
  let highlightedActorIndex = -1;
  let highlightedActorUntil = 0;
  let minimapIsDragging = false;
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
      <rect class="qr-bg" width="${viewBoxSize}" height="${viewBoxSize}" fill="#0a0e17" rx="1"/>
      <path class="qr-fg" d="${path.trim()}" fill="#00ffcc"/>
    </svg>`;
  }

  // -------------------------------------------------------------------------
  // UI Initialization
  // -------------------------------------------------------------------------
  function renderQRCodeInto(container, targetUrl, fallbackText) {
    if (!container || !targetUrl) return;
    try {
      const svg = generateQRCodeSVG(targetUrl);
      const doc = new DOMParser().parseFromString(svg, 'image/svg+xml');
      if (!doc || !doc.documentElement || doc.querySelector('parsererror')) {
        throw new Error('SVG parse error');
      }
      container.replaceChildren(doc.documentElement);
      container.addEventListener('click', function() {
        window.open(targetUrl, '_blank', 'noopener,noreferrer');
      });
    } catch (err) {
      console.error('Failed to generate offline QR code:', err);
      const fallback = document.createElement('div');
      fallback.className = 'qr-fallback';
      fallback.textContent = fallbackText;
      container.replaceChildren(fallback);
    }
  }

  function initOnboardingQR() {
    const chatUrl = window.location.origin + '/chat';
    if (el.qrUrlText) {
      el.qrUrlText.textContent = chatUrl;
    }
    if (el.qrLink) {
      el.qrLink.href = chatUrl;
    }
    renderQRCodeInto(el.qrCode, chatUrl, 'Scan /chat on mobile');
  }

  function initGitHubRepoQR() {
    if (!el.githubRepoLink || !el.githubQrCode) return;
    renderQRCodeInto(el.githubQrCode, el.githubRepoLink.href, 'github.com/moficodes/agent-swarm');
  }

  // -------------------------------------------------------------------------
  // Telemetry Updates
  // -------------------------------------------------------------------------
  let isTelemetryFetching = false;
  let currentAtespace = 'ate-demo-conf';
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
    if (data.atespace) {
      currentAtespace = data.atespace;
    }

    const seats = data.seats_per_worker > 0 ? data.seats_per_worker : 4;
    const p = data.worker_pods || {};
    latestActorsMap = data.actors || {};
    latestFleetCompact = data.fleet_compact || '';
    latestActorCount = data.actor_count || 0;
    latestSeatsPerWorker = seats;
    latestSeatsTotal = (p.available && p.current > 0) ? (p.current * seats) : 0;
    latestRunningActors = data.running_actors || 0;
    latestSuspendedActors = data.suspended_actors || 0;

    updateSimulationTargets(latestActorCount, latestSeatsTotal);

    // 1. Update Envoy L7 Parking Meter
    updateParkingMeter(data.parked_requests || 0, data.parking_available);

    // 2. Update Worker Pod Cards
    updateWorkerCards(data.workers || [], seats, data.worker_pods);

    // 3. Update Attendee Fleet Badges
    renderFleetGrid(data.actors || {});

    // 4. Update Event Feed
    updateEventTicker(data.recent_events || []);

    // 5. Update Mock LLM Banner
    updateMockBanner(!!data.mock_llm);

    // 6. Update the live pod count and oversubscription header
    const busySeats = (data.workers || []).reduce((n, w) => n + (w.actors || []).length, 0);
    updateWorkerPods(data.worker_pods, data.actor_count || 0, seats, busySeats,
      data.parked_requests || 0, data.parking_available);

    // 7. Update the agent workspace volume header card
    updateWorkspaceSpec(data.actors || {});
  }

  // formatRatio shows one decimal below 10x, so 50 actors on 20 seats reads
  // 2.5x rather than rounding to 3x.
  function formatRatio(r) {
    return (r < 10 ? String(Math.round(r * 10) / 10) : String(Math.round(r))) + 'x';
  }

  // updateWorkerPods fills the header's pod count, seat count and ratio.
  // They show '--' when the pool's size is unknown or zero, so the ratio
  // never divides by 0. Each pod hosts seatsPerWorker actors at once, so the
  // ratio is actors per seat, not per pod.
  function updateWorkerPods(pods, actorCount, seatsPerWorker, busySeats, parkedCount, parkingAvailable) {
    if (!el.workerPodsCurrent) return;
    const p = pods || {};
    const ok = !!p.available && p.current > 0;
    const totalSeats = ok ? p.current * seatsPerWorker : 0;

    el.workerPodsCurrent.textContent = ok ? String(p.current) : '--';
    el.workerPodsMax.textContent = ok && p.max > 0 ? 'of ' + p.max : '';
    if (el.workerPodsSeats) {
      el.workerPodsSeats.textContent = ok ? totalSeats + ' seats (' + seatsPerWorker + ' per pod)' : '';
    }
    el.oversubRatio.textContent = ok && actorCount > 0 ? formatRatio(actorCount / totalSeats) : '--';
    if (el.workerPoolReplicas) {
      el.workerPoolReplicas.textContent = ok
        ? p.current + (p.current === 1 ? ' Replica' : ' Replicas') + ' // ' + busySeats + ' of ' + totalSeats + ' Execution Seats Busy'
        : 'Execution Seats';
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

    // Compute progress gauge bar
    const maxScale = latestSeatsTotal > 50 ? Math.min(4096, latestSeatsTotal) : 50;
    const pct = Math.min(100, Math.max(0, Math.round((parkedCount / maxScale) * 100)));
    if (el.parkingProgressBar) {
      el.parkingProgressBar.style.width = pct + '%';
    }
    const markers = el.parkingMeter ? el.parkingMeter.querySelectorAll('.gauge-markers span') : null;
    if (markers && markers.length === 4) {
      markers[0].textContent = '0';
      markers[1].textContent = String(Math.round(maxScale * 0.2));
      markers[2].textContent = String(Math.round(maxScale * 0.5));
      markers[3].textContent = maxScale.toLocaleString() + ' MAX';
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
  // configured without a real Gemini backend.
  function updateMockBanner(mockLLM) {
    if (el.mockBanner) el.mockBanner.hidden = !mockLLM;
  }

  // updateWorkspaceSpec populates the header's agent workspace card from live
  // external volume bindings across reported actors.
  function updateWorkspaceSpec(actorsMap) {
    if (!el.workspaceMode) return;
    const summary = window.ActorView.fleetVolumes(actorsMap);
    el.workspaceMode.textContent = summary.label;
    el.workspaceMode.className = 'spec-value' + (summary.configured ? ' highlight-cyan' : '');
    if (el.workspaceBound) {
      const showSub = summary.known && !!summary.sub;
      el.workspaceBound.hidden = !showSub;
      el.workspaceBound.textContent = showSub ? summary.sub : '';
      el.workspaceBound.className = 'spec-note' + (summary.configured ? ' spec-note-cyan' : '');
    }
  }

  const BADGE_PRIORITY = {
    problem: 8,
    executing: 7,
    awaiting: 6,
    thinking: 5,
    running: 4,
    resuming: 3,
    suspending: 2,
    suspended: 1,
    'never-started': 0,
    unknown: 0
  };

  function actorBadgeForWorkerActor(wa) {
    if (!wa) return null;
    const full = latestActorsMap[wa.actor_id];
    if (full) {
      return window.ActorView.badge(full);
    }
    return window.ActorView.badge({ state: wa.state });
  }

  function dominantPodClass(actors) {
    if (!actors || actors.length === 0) return 'empty';
    let bestCls = 'running';
    let bestRank = -1;
    for (let i = 0; i < actors.length; i++) {
      const b = actorBadgeForWorkerActor(actors[i]);
      const cls = b ? b.cls : 'running';
      const rank = BADGE_PRIORITY[cls] !== undefined ? BADGE_PRIORITY[cls] : 1;
      if (rank > bestRank) {
        bestRank = rank;
        bestCls = cls;
      }
    }
    return bestCls;
  }

  const ZOOM_CORNER_ICON_SVG = '<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><polyline points="15 3 21 3 21 9"/><polyline points="9 21 3 21 3 15"/><line x1="21" y1="3" x2="14" y2="10"/><line x1="3" y1="21" x2="10" y2="14"/></svg>';
  const BACK_ARROW_ICON_SVG = '<svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="19" y1="12" x2="5" y2="12"/><polyline points="12 19 5 12 12 5"/></svg>';

  function shortPodName(podName) {
    if (!podName) return 'worker';
    const parts = podName.split('-');
    if (parts.length >= 2) {
      return parts.slice(-2).join('-');
    }
    return podName;
  }

  function parseActorNumber(actorId) {
    if (!actorId) return 0;
    const m = String(actorId).match(/(\d+)$/);
    return m ? parseInt(m[1], 10) : 0;
  }

  function isActorLeased(actorId, actorNum) {
    const live = actorId ? latestActorsMap[actorId] : null;
    if (live && live.leased) return true;
    const idx = actorNum || parseActorNumber(actorId);
    if (idx >= 1 && latestFleetCompact && idx <= latestFleetCompact.length) {
      const raw = latestFleetCompact.charCodeAt(idx - 1) - 64;
      return (raw & 16) !== 0;
    }
    return false;
  }

  function shortStatePillLabel(cls, fallback) {
    switch (cls) {
      case 'thinking': return 'THINKING';
      case 'executing': return 'EXECUTING';
      case 'awaiting': return 'AWAITING';
      case 'running': return 'RUNNING';
      case 'resuming': return 'RESUMING';
      case 'suspending': return 'SUSPENDING';
      case 'problem': return 'PROBLEM';
      default: return (fallback || 'ACTIVE').toUpperCase();
    }
  }

  function describeSeatActivity(wa, badgeObj) {
    const cls = badgeObj ? badgeObj.cls : 'running';
    switch (cls) {
      case 'thinking': return 'Gemini 3.8 Flash';
      case 'executing': return 'gVisor Sandbox';
      case 'awaiting': return 'Human Review Gate';
      case 'resuming': return 'Restoring GCS Snap';
      case 'suspending': return 'Writing GCS Snap';
      case 'running': return 'Active Pod Seat';
      default: return (badgeObj && badgeObj.sub) ? badgeObj.sub : 'Seated on Worker';
    }
  }

  function render4x4SeatWorkerCardHTML(w, seats, nodeName) {
    const actors = w.actors || [];
    const podName = w.pod_name || 'worker';
    const shortPod = shortPodName(podName);
    const isBusy = actors.length > 0;
    const isHighlighted = topoHighlightPod && topoHighlightPod === podName;
    const nAttr = nodeName ? ` data-node="${escapeHTML(nodeName)}"` : '';
    const cells = [];
    for (let i = 0; i < seats; i++) {
      const wa = actors[i];
      if (!wa) {
        cells.push(
          `<div class="seat-cell empty" data-hover-kind="seat" data-pod="${escapeHTML(podName)}"${nAttr} data-seat="${i + 1}">·</div>`
        );
      } else {
        const b = actorBadgeForWorkerActor(wa);
        const cls = b ? b.cls : 'running';
        const numMatch = (wa.actor_id || '').match(/(\d+)$/);
        const shortNum = numMatch ? numMatch[1] : '•';
        cells.push(
          `<div class="seat-cell ${cls}" data-hover-kind="seat" data-pod="${escapeHTML(podName)}"${nAttr} data-seat="${i + 1}" data-actor="${escapeHTML(wa.actor_id || '')}">${escapeHTML(shortNum.slice(-3))}</div>`
        );
      }
    }
    return `
      <div class="card worker-card zoomable-card${isBusy ? ' active' : ''}${isHighlighted ? ' pod-highlight-flash' : ''}" data-pod-card="${escapeHTML(podName)}"${nAttr}>
        <div class="worker-card-header" data-zoom-pod="${escapeHTML(podName)}"${nAttr} title="Click to zoom into pod ${escapeHTML(podName)} and inspect all ${seats} actor seats">
          <div class="worker-id">
            <span class="worker-dot${isBusy ? ' active' : ''}"></span>
            <span class="worker-title">${escapeHTML(shortPod)}</span>
          </div>
          <div class="worker-header-right">
            <span class="worker-seat-count${actors.length >= seats ? ' full' : ''}">${actors.length}/${seats}</span>
            <button type="button" class="node-corner-zoom-btn" data-zoom-pod="${escapeHTML(podName)}"${nAttr} title="Zoom into pod ${escapeHTML(podName)} (see all ${seats} actors)" aria-label="Zoom into pod ${escapeHTML(shortPod)}">${ZOOM_CORNER_ICON_SVG}</button>
          </div>
        </div>
        <div class="seat-grid-4x4">${cells.join('')}</div>
      </div>
    `;
  }

  function renderNodeMatrixCardHTML(nodeGroup, nodeIdx, seatsPerPod) {
    const pods = nodeGroup.pods || [];
    const nodeName = nodeGroup.nodeName || ('node-' + String(nodeIdx + 1).padStart(2, '0'));
    const nodeLabel = nodeGroup.nodeLabel || ('node-' + String(nodeIdx + 1).padStart(2, '0'));
    const totalNodeSeats = pods.length * seatsPerPod;
    let busyNodeSeats = 0;
    const cells = [];
    for (let i = 0; i < 16; i++) {
      const w = pods[i];
      if (!w) {
        cells.push('<div class="seat-cell empty">·</div>');
        continue;
      }
      const actors = w.actors || [];
      busyNodeSeats += actors.length;
      const cls = dominantPodClass(actors);
      const firstActor = actors.length > 0 ? (actors[0].actor_id || '') : '';
      cells.push(
        `<div class="seat-cell ${cls}" data-hover-kind="pod" data-pod="${escapeHTML(w.pod_name)}" data-node="${escapeHTML(nodeName)}" data-actor="${escapeHTML(firstActor)}">${actors.length}</div>`
      );
    }
    const isBusy = busyNodeSeats > 0;
    return `
      <div class="card worker-card zoomable-card${isBusy ? ' active' : ''}" data-node-card="${escapeHTML(nodeName)}">
        <div class="worker-card-header" data-zoom-node="${escapeHTML(nodeName)}" title="Click to zoom into GKE Node ${escapeHTML(nodeLabel)} (${escapeHTML(nodeName)}) and see all 16 pods with their slots">
          <div class="worker-id">
            <span class="worker-dot${isBusy ? ' active' : ''}"></span>
            <span class="worker-title">${escapeHTML(nodeLabel)}</span>
          </div>
          <div class="worker-header-right">
            <span class="worker-seat-count${busyNodeSeats >= totalNodeSeats ? ' full' : ''}">${busyNodeSeats}/${totalNodeSeats}</span>
            <button type="button" class="node-corner-zoom-btn" data-zoom-node="${escapeHTML(nodeName)}" title="Zoom into ${escapeHTML(nodeLabel)} (see all 16 pods &amp; their 16 slots)" aria-label="Zoom into ${escapeHTML(nodeLabel)}">${ZOOM_CORNER_ICON_SVG}</button>
          </div>
        </div>
        <div class="seat-grid-4x4">${cells.join('')}</div>
      </div>
    `;
  }

  function renderPodActorSeatDetailCardHTML(w, seatIdx, seatsPerPod, nodeGroup) {
    const actors = (w && w.actors) ? w.actors : [];
    const podName = (w && w.pod_name) ? w.pod_name : 'worker';
    const nodeName = (nodeGroup && nodeGroup.nodeName) ? nodeGroup.nodeName : '';
    const wa = actors[seatIdx];
    const seatLabel = 'SEAT ' + String(seatIdx + 1).padStart(2, '0');
    if (!wa || !wa.actor_id) {
      return `
        <div class="pod-actor-seat-card empty" data-hover-kind="seat" data-pod="${escapeHTML(podName)}" data-node="${escapeHTML(nodeName)}" data-seat="${seatIdx + 1}">
          <div class="seat-card-top">
            <span class="seat-card-idx">${seatLabel}</span>
            <span class="seat-card-badge empty">IDLE</span>
          </div>
          <div class="seat-card-main">
            <span class="seat-card-actor">Open Seat</span>
          </div>
          <div class="seat-card-bottom">
            <span class="seat-card-sub">Ready for actor wake</span>
          </div>
        </div>
      `;
    }
    const actorId = wa.actor_id;
    const b = actorBadgeForWorkerActor(wa) || { cls: 'running', label: 'Running', sub: '' };
    const cls = b.cls || 'running';
    const pillText = shortStatePillLabel(cls, b.label);
    const activityDetail = describeSeatActivity(wa, b);
    const actorNum = parseActorNumber(actorId);
    const isQR = actorNum > 0 && (actorNum % 16 === 0);
    const leased = isActorLeased(actorId, actorNum);
    const qrBadge = leased
      ? '<span class="seat-card-qr leased" title="Active live QR attendee lease">LIVE</span>'
      : (isQR ? '<span class="seat-card-qr" title="Multiple-of-16 QR-reservable attendee slot">QR 16x</span>' : '');
    return `
      <div class="pod-actor-seat-card ${cls}" data-hover-kind="seat" data-pod="${escapeHTML(podName)}" data-node="${escapeHTML(nodeName)}" data-seat="${seatIdx + 1}" data-actor="${escapeHTML(actorId)}">
        <div class="seat-card-top">
          <span class="seat-card-idx">${seatLabel}</span>
          <span class="seat-card-badge ${cls}">${escapeHTML(pillText)}</span>
        </div>
        <div class="seat-card-main">
          <span class="seat-card-actor" title="${escapeHTML(actorId)}">${escapeHTML(formatActorName(actorId))}</span>
          ${qrBadge}
        </div>
        <div class="seat-card-bottom">
          <span class="seat-card-sub">${escapeHTML(activityDetail)}</span>
          <button type="button" class="btn-locate-actor" data-locate-actor="${escapeHTML(actorId)}" title="Zoom to ${escapeHTML(formatActorName(actorId))} in the 65,536 fleet matrix">65k ⌖</button>
        </div>
      </div>
    `;
  }

  function renderWorkerTopoNav(seatsPerPod, multiNodeMode) {
    if (!el.workerTopoNav) return;
    if (!multiNodeMode && !topoSelectedPod) {
      el.workerTopoNav.innerHTML = '';
      return;
    }

    if (!topoSelectedNode && !topoSelectedPod) {
      el.workerTopoNav.innerHTML = `
        <span class="topo-hint-pill">16 Nodes × 16 Pods × 16 Seats · Click any node corner to zoom into its 16 pods</span>
      `;
      return;
    }

    const activeNode = latestNodeGroups.find((g) => g.nodeName === topoSelectedNode) || latestNodeGroups[0] || null;
    if (topoSelectedNode && !topoSelectedPod && activeNode) {
      const pods = activeNode.pods || [];
      const totalNodeSeats = pods.length * seatsPerPod;
      const busyNodeSeats = pods.reduce((sum, p) => sum + ((p.actors || []).length), 0);
      el.workerTopoNav.innerHTML = `
        <button type="button" class="topo-crumb-btn" data-topo-action="all-nodes">${BACK_ARROW_ICON_SVG}<span>All 16 Nodes</span></button>
        <span class="topo-crumb-sep">▸</span>
        <span class="topo-crumb-active" title="GKE Node: ${escapeHTML(activeNode.nodeName)}">
          <span>${escapeHTML(activeNode.nodeLabel)}</span>
          <span class="topo-crumb-meta">${pods.length} Pods · ${busyNodeSeats}/${totalNodeSeats} Seats</span>
        </span>
        <span class="topo-step-group" role="group" aria-label="Cycle GKE nodes">
          <button type="button" class="topo-step-btn" data-topo-action="prev-node" title="Previous GKE Node" aria-label="Previous GKE Node">‹</button>
          <button type="button" class="topo-step-btn" data-topo-action="next-node" title="Next GKE Node" aria-label="Next GKE Node">›</button>
        </span>
        <span class="topo-hint-pill">Click any pod to see all ${seatsPerPod} actors in that pod</span>
      `;
      return;
    }

    if (topoSelectedPod) {
      const podInfo = podHoverDirectory[topoSelectedPod];
      const podActors = (podInfo && podInfo.actors) ? podInfo.actors : [];
      const nodeForPod = activeNode || latestNodeGroups.find((g) => (g.pods || []).some((p) => p.pod_name === topoSelectedPod)) || null;
      const nodeCrumb = (multiNodeMode && nodeForPod)
        ? `<button type="button" class="topo-crumb-btn" data-topo-action="zoom-node" data-node="${escapeHTML(nodeForPod.nodeName)}">${escapeHTML(nodeForPod.nodeLabel)} (16 Pods)</button><span class="topo-crumb-sep">▸</span>`
        : '';
      const rootLabel = multiNodeMode ? 'All 16 Nodes' : 'All Pods';
      el.workerTopoNav.innerHTML = `
        <button type="button" class="topo-crumb-btn" data-topo-action="all-nodes">${BACK_ARROW_ICON_SVG}<span>${rootLabel}</span></button>
        <span class="topo-crumb-sep">▸</span>
        ${nodeCrumb}
        <span class="topo-crumb-active" title="Worker Pod: ${escapeHTML(topoSelectedPod)}">
          <span>Pod ${escapeHTML(shortPodName(topoSelectedPod))}</span>
          <span class="topo-crumb-meta">${podActors.length}/${seatsPerPod} Active Actor Seats</span>
        </span>
        <span class="topo-step-group" role="group" aria-label="Cycle worker pods">
          <button type="button" class="topo-step-btn" data-topo-action="prev-pod" title="Previous Worker Pod" aria-label="Previous Worker Pod">‹</button>
          <button type="button" class="topo-step-btn" data-topo-action="next-pod" title="Next Worker Pod" aria-label="Next Worker Pod">›</button>
        </span>
        <span class="topo-hint-pill">Click any actor seat for Spotlight · Click 65k ⌖ to zoom in fleet matrix</span>
      `;
    }
  }

  function zoomToTopologyNode(nodeName, highlightPodName) {
    if (!nodeName) return;
    topoSelectedNode = nodeName;
    topoSelectedPod = null;
    topoHighlightPod = highlightPodName || null;
    hideHoverTooltip();
    updateWorkerCards(latestWorkerList, latestWorkerSeats, latestWorkerPodsMeta);
  }

  function zoomToTopologyPod(podName, nodeName) {
    if (!podName) return;
    topoSelectedPod = podName;
    if (nodeName) {
      topoSelectedNode = nodeName;
    } else if (podHoverDirectory[podName] && podHoverDirectory[podName].nodeName) {
      topoSelectedNode = podHoverDirectory[podName].nodeName;
    }
    topoHighlightPod = null;
    hideHoverTooltip();
    updateWorkerCards(latestWorkerList, latestWorkerSeats, latestWorkerPodsMeta);
  }

  function resetTopologyZoom() {
    topoSelectedNode = null;
    topoSelectedPod = null;
    topoHighlightPod = null;
    hideHoverTooltip();
    updateWorkerCards(latestWorkerList, latestWorkerSeats, latestWorkerPodsMeta);
  }

  function stepTopologyNode(delta) {
    if (!latestNodeGroups || latestNodeGroups.length === 0) return;
    const idx = latestNodeGroups.findIndex((g) => g.nodeName === topoSelectedNode);
    const nextIdx = idx < 0 ? 0 : (idx + delta + latestNodeGroups.length) % latestNodeGroups.length;
    zoomToTopologyNode(latestNodeGroups[nextIdx].nodeName);
  }

  function stepTopologyPod(delta) {
    let podPool = [];
    const activeNode = latestNodeGroups.find((g) => g.nodeName === topoSelectedNode);
    if (activeNode && Array.isArray(activeNode.pods) && activeNode.pods.length > 0) {
      podPool = activeNode.pods;
    } else {
      podPool = latestWorkerList || [];
    }
    if (podPool.length === 0) return;
    const idx = podPool.findIndex((p) => p.pod_name === topoSelectedPod);
    const nextIdx = idx < 0 ? 0 : (idx + delta + podPool.length) % podPool.length;
    zoomToTopologyPod(podPool[nextIdx].pod_name, topoSelectedNode);
  }

  // renderSeatHTML renders one of a worker's seats: the actor holding it and
  // the actor's substrate state, or an idle placeholder.
  function renderSeatHTML(a) {
    if (!a) {
      return '<li class="worker-seat empty"><span class="seat-actor">idle</span></li>';
    }
    const s = window.ActorView.state({ state: a.state });
    const running = s.cls === 'running';
    return `
      <li class="worker-seat">
        <span class="seat-actor${running ? ' running' : ''}" title="${escapeHTML(a.actor_id || '')}">${escapeHTML(formatActorName(a.actor_id))}</span>
        <span class="worker-status-badge ${s.cls}">${escapeHTML(s.label)}</span>
      </li>
    `;
  }

  // renderWorkerCardHTML renders one real worker pod: its name, how many of
  // its seats are taken, and one row per seat.
  function renderWorkerCardHTML(w, seats, compact) {
    const actors = w.actors || [];
    const podName = w.pod_name || 'worker';
    // Compact cards keep only the pod name's random suffix, the part that
    // tells workers apart.
    const title = compact ? podName.split('-').pop() : podName;
    const isBusy = actors.length > 0;
    const rows = [];
    for (let i = 0; i < Math.max(seats, actors.length); i++) rows.push(renderSeatHTML(actors[i]));
    return `
      <div class="card worker-card${isBusy ? ' active' : ''}">
        <div class="worker-card-header">
          <div class="worker-id">
            <span class="worker-dot${isBusy ? ' active' : ''}"></span>
            <span class="worker-title" title="${escapeHTML(podName)}">${escapeHTML(title)}</span>
          </div>
          <span class="worker-seat-count${actors.length >= seats ? ' full' : ''}">${actors.length}/${seats}</span>
        </div>
        <ul class="worker-seats">${rows.join('')}</ul>
      </div>
    `;
  }

  function updateWorkerCards(workers, seats, workerPods) {
    if (!el.workerList) return;
    latestWorkerList = (workers || []).slice();
    latestWorkerSeats = seats;
    latestWorkerPodsMeta = workerPods;

    const list = (workers || []).slice();
    const podCount = (workerPods && workerPods.available && workerPods.current > 0) ? workerPods.current : 2;
    const targetCount = Math.max(2, list.length, podCount);
    const podNames = (workerPods && Array.isArray(workerPods.pod_names)) ? workerPods.pod_names : [];
    const podNodes = (workerPods && workerPods.pod_nodes) ? workerPods.pod_nodes : {};
    const seen = new Set(list.map((w) => w.pod_name));
    for (let i = 0; i < podNames.length && list.length < targetCount; i++) {
      if (podNames[i] && !seen.has(podNames[i])) {
        seen.add(podNames[i]);
        list.push({ pod_name: podNames[i], actors: [] });
      }
    }
    let idx = 1;
    while (list.length < targetCount) {
      const fallbackName = 'worker-' + idx++;
      if (!seen.has(fallbackName)) {
        seen.add(fallbackName);
        list.push({ pod_name: fallbackName, actors: [] });
      }
    }
    list.sort((a, b) => (a.pod_name || '').localeCompare(b.pod_name || ''));

    podHoverDirectory = {};
    seatedActorPodMap = {};
    for (let i = 0; i < list.length; i++) {
      const w = list[i];
      const nName = podNodes[w.pod_name] || '';
      const pActors = w.actors || [];
      podHoverDirectory[w.pod_name] = {
        podName: w.pod_name,
        nodeName: nName,
        actors: pActors,
        seats: seats
      };
      const sPod = shortPodName(w.pod_name);
      for (let sIdx = 0; sIdx < pActors.length; sIdx++) {
        const wa = pActors[sIdx];
        if (wa && wa.actor_id) {
          seatedActorPodMap[wa.actor_id] = {
            podName: w.pod_name,
            shortPod: sPod,
            nodeName: nName,
            seatIndex: sIdx + 1,
            state: wa.state || 'RUNNING'
          };
        }
      }
    }

    // 16-Node x 16-Pod x 16-Seat high-scale cluster mode (> 16 worker pods):
    if (list.length > 16) {
      const byNode = new Map();
      const unassigned = [];
      for (let i = 0; i < list.length; i++) {
        const w = list[i];
        const nName = podNodes[w.pod_name];
        if (nName) {
          if (!byNode.has(nName)) byNode.set(nName, []);
          byNode.get(nName).push(w);
        } else {
          unassigned.push(w);
        }
      }
      const nodeNames = Array.from(byNode.keys()).sort();
      const nodeGroups = nodeNames.map((nName) => ({ nodeName: nName, pods: byNode.get(nName) }));
      while (unassigned.length > 0) {
        const chunk = unassigned.splice(0, 16);
        nodeGroups.push({
          nodeName: 'node-' + String(nodeGroups.length + 1).padStart(2, '0'),
          pods: chunk
        });
      }
      latestNodeGroups = nodeGroups.slice(0, 16).map((g, i) => ({
        nodeName: g.nodeName,
        nodeLabel: 'node-' + String(i + 1).padStart(2, '0'),
        nodeIndex: i,
        pods: g.pods || []
      }));

      // Backfill assigned nodeName/nodeLabel onto podHoverDirectory & seatedActorPodMap
      for (let nIdx = 0; nIdx < latestNodeGroups.length; nIdx++) {
        const ng = latestNodeGroups[nIdx];
        for (let pIdx = 0; pIdx < ng.pods.length; pIdx++) {
          const pw = ng.pods[pIdx];
          if (podHoverDirectory[pw.pod_name]) {
            podHoverDirectory[pw.pod_name].nodeName = ng.nodeName;
            podHoverDirectory[pw.pod_name].nodeLabel = ng.nodeLabel;
          }
          const pActors = pw.actors || [];
          for (let aIdx = 0; aIdx < pActors.length; aIdx++) {
            const wa = pActors[aIdx];
            if (wa && wa.actor_id && seatedActorPodMap[wa.actor_id]) {
              seatedActorPodMap[wa.actor_id].nodeName = ng.nodeName;
              seatedActorPodMap[wa.actor_id].nodeLabel = ng.nodeLabel;
            }
          }
        }
      }

      if (topoSelectedPod && !podHoverDirectory[topoSelectedPod]) {
        topoSelectedPod = null;
      }
      if (topoSelectedNode && !latestNodeGroups.some((g) => g.nodeName === topoSelectedNode)) {
        topoSelectedNode = null;
      }
      if (topoSelectedPod && !topoSelectedNode) {
        const ownerNode = latestNodeGroups.find((g) => g.pods.some((p) => p.pod_name === topoSelectedPod));
        if (ownerNode) topoSelectedNode = ownerNode.nodeName;
      }

      renderWorkerTopoNav(seats, true);
      el.workerList.classList.add('compact', 'node-matrix-grid');

      // Level 2 Drill-Down: Single Pod -> 16 Actor Seat Cards (8x2 grid taking entire space)
      if (topoSelectedPod) {
        const ownerNode = latestNodeGroups.find((g) => g.pods.some((p) => p.pod_name === topoSelectedPod)) || null;
        const activePod = (ownerNode && ownerNode.pods.find((p) => p.pod_name === topoSelectedPod)) ||
          list.find((p) => p.pod_name === topoSelectedPod) ||
          { pod_name: topoSelectedPod, actors: [] };
        el.workerList.style.gridTemplateColumns = 'repeat(8, minmax(0, 1fr))';
        const seatCards = [];
        for (let sIdx = 0; sIdx < seats; sIdx++) {
          seatCards.push(renderPodActorSeatDetailCardHTML(activePod, sIdx, seats, ownerNode));
        }
        el.workerList.innerHTML = seatCards.join('');
        return;
      }

      // Level 1 Drill-Down: Single Node -> 16 Pod Cards with their 4x4 actor slots (8x2 grid taking entire space)
      if (topoSelectedNode) {
        const activeNode = latestNodeGroups.find((g) => g.nodeName === topoSelectedNode) || latestNodeGroups[0];
        const nodePods = (activeNode && activeNode.pods) ? activeNode.pods.slice(0, 16) : [];
        const cols = Math.min(8, Math.max(4, Math.ceil(Math.max(1, nodePods.length) / 2)));
        el.workerList.style.gridTemplateColumns = 'repeat(' + cols + ', minmax(0, 1fr))';
        el.workerList.innerHTML = nodePods.map((w) => render4x4SeatWorkerCardHTML(w, seats, activeNode.nodeName)).join('');
        return;
      }

      // Level 0 Overview: All 16 Nodes (8x2 grid, each showing 4x4 pods)
      const cols = Math.min(8, Math.max(4, Math.ceil(latestNodeGroups.length / 2)));
      el.workerList.style.gridTemplateColumns = 'repeat(' + cols + ', minmax(0, 1fr))';
      el.workerList.innerHTML = latestNodeGroups.map((g, i) => renderNodeMatrixCardHTML(g, i, seats)).join('');
      return;
    }

    // Up to 16 worker pods, each with 16 actor seats (4x4 seat matrix per pod card):
    latestNodeGroups = [];
    if (seats === 16) {
      if (topoSelectedPod && !podHoverDirectory[topoSelectedPod]) {
        topoSelectedPod = null;
      }
      renderWorkerTopoNav(seats, false);
      el.workerList.classList.add('compact', 'node-matrix-grid');
      if (topoSelectedPod) {
        const activePod = list.find((p) => p.pod_name === topoSelectedPod) || { pod_name: topoSelectedPod, actors: [] };
        el.workerList.style.gridTemplateColumns = 'repeat(8, minmax(0, 1fr))';
        const seatCards = [];
        for (let sIdx = 0; sIdx < seats; sIdx++) {
          seatCards.push(renderPodActorSeatDetailCardHTML(activePod, sIdx, seats, null));
        }
        el.workerList.innerHTML = seatCards.join('');
        return;
      }
      const cols = list.length > 8 ? 8 : list.length;
      el.workerList.style.gridTemplateColumns = 'repeat(' + cols + ', minmax(0, 1fr))';
      el.workerList.innerHTML = list.map((w) => render4x4SeatWorkerCardHTML(w, seats, '')).join('');
      return;
    }

    // Standard mode (2..16 worker pods with list-based seat rows):
    renderWorkerTopoNav(seats, false);
    const compact = list.length > 2;
    el.workerList.classList.remove('node-matrix-grid');
    el.workerList.classList.toggle('compact', compact);
    el.workerList.style.gridTemplateColumns = 'repeat(' + list.length + ', minmax(0, 1fr))';
    el.workerList.innerHTML = list.map((w) => renderWorkerCardHTML(w, seats, compact)).join('');
  }

  function formatActorName(id) {
    if (!id) return 'idle';
    const match = id.match(/attendee-(\d+)/i);
    if (match) {
      return 'Attendee ' + match[1].padStart(2, '0');
    }
    return id;
  }

  const CANVAS_COLORS_DARK = [
    '#1e293b', // 0: never-started
    '#0284c7', // 1: suspended (snapshot saved)
    '#00ffcc', // 2: resuming
    '#00ffcc', // 3: suspending
    '#10b981', // 4: running
    '#ec4899', // 5: thinking
    '#a855f7', // 6: awaiting approval
    '#f59e0b', // 7: executing
    '#ef4444', // 8: problem
    '#334155'  // 9: unknown
  ];

  const CANVAS_COLORS_LIGHT = [
    '#e2e8f0', // 0: never-started
    '#38bdf8', // 1: suspended (snapshot saved)
    '#0284c7', // 2: resuming
    '#0284c7', // 3: suspending
    '#059669', // 4: running
    '#db2777', // 5: thinking
    '#7c3aed', // 6: awaiting approval
    '#d97706', // 7: executing
    '#dc2626', // 8: problem
    '#cbd5e1'  // 9: unknown
  ];

  const STATE_SHORT_LABELS = [
    'IDLE', // 0
    'GCS',  // 1
    'WAKE', // 2
    'SAVE', // 3
    'RUN',  // 4
    'THINK',// 5
    'WAIT', // 6
    'EXEC', // 7
    'ERR',  // 8
    'UNK'   // 9
  ];

  const STATE_FULL_LABELS = [
    'READY',     // 0
    'GCS SNAP',  // 1
    'RESUMING',  // 2
    'SUSPEND',   // 3
    'RUNNING',   // 4
    'THINKING',  // 5
    'AWAITING',  // 6
    'EXECUTING', // 7
    'CRASHED',   // 8
    'UNKNOWN'    // 9
  ];

  let hoveredCanvasIndex = -1;

  function computeCanvasDims(total) {
    if (total === 65536) {
      return { cols: 512, rows: 128 };
    }
    const cols = Math.max(32, Math.ceil(Math.sqrt(total * 3.5)));
    const rows = Math.max(8, Math.ceil(total / cols));
    return { cols: cols, rows: rows };
  }

  function clampFleetPan() {
    if (fleetZoom <= 1.001) {
      fleetZoom = 1;
      fleetPanX = 0;
      fleetPanY = 0;
      return;
    }
    if (fleetZoom > 32) fleetZoom = 32;
    const maxPan = Math.max(0, 1 - 1 / fleetZoom);
    fleetPanX = Math.max(0, Math.min(maxPan, fleetPanX));
    fleetPanY = Math.max(0, Math.min(maxPan, fleetPanY));
  }

  function syncFleetZoomToolbarUI() {
    const zText = (fleetZoom < 10 ? fleetZoom.toFixed(1).replace(/\.0$/, '') : String(Math.round(fleetZoom))) + '×';
    if (el.fleetZoomReadout) {
      el.fleetZoomReadout.textContent = zText;
    }
    if (el.btnFleetZoomReset) {
      el.btnFleetZoomReset.hidden = fleetZoom <= 1.01;
    }
    if (el.fleetCanvas) {
      el.fleetCanvas.classList.toggle('is-zoomed', fleetZoom > 1.01);
    }
    if (el.fleetZoomPresets) {
      el.fleetZoomPresets.forEach((btn) => {
        const target = parseFloat(btn.dataset.zoom || '1');
        const active = Math.abs(fleetZoom - target) < 0.15;
        btn.classList.toggle('active', active);
      });
    }
    if (el.btnFleetLoupe) {
      el.btnFleetLoupe.classList.toggle('active', fleetLoupeEnabled);
      el.btnFleetLoupe.setAttribute('aria-pressed', fleetLoupeEnabled ? 'true' : 'false');
    }
    if (el.fleetMinimapWrap) {
      el.fleetMinimapWrap.hidden = fleetZoom <= 1.02;
    }
  }

  function setFleetZoom(newZoom, anchorUVX, anchorUVY) {
    const clamped = Math.max(1, Math.min(32, newZoom));
    const ux = typeof anchorUVX === 'number' ? Math.max(0, Math.min(1, anchorUVX)) : 0.5;
    const uy = typeof anchorUVY === 'number' ? Math.max(0, Math.min(1, anchorUVY)) : 0.5;
    const worldUVX = fleetPanX + ux / fleetZoom;
    const worldUVY = fleetPanY + uy / fleetZoom;
    fleetZoom = clamped;
    fleetPanX = worldUVX - ux / fleetZoom;
    fleetPanY = worldUVY - uy / fleetZoom;
    clampFleetPan();
    syncFleetZoomToolbarUI();
    if (latestFleetCompact && latestFleetCompact.length > 256) {
      renderFleetCanvas(latestFleetCompact, latestActorCount || latestFleetCompact.length);
    }
  }

  function focusActorOnFleetCanvas(actorIdOrIdx, targetZoom) {
    const total = latestActorCount || (latestFleetCompact ? latestFleetCompact.length : 65536);
    const idx1 = typeof actorIdOrIdx === 'number' ? actorIdOrIdx : parseActorNumber(actorIdOrIdx);
    if (!idx1 || idx1 < 1 || idx1 > total) return;
    const dims = computeCanvasDims(total);
    canvasCols = dims.cols;
    canvasRows = dims.rows;
    const idx0 = idx1 - 1;
    const row = Math.floor(idx0 / canvasCols);
    const col = idx0 % canvasCols;
    fleetZoom = Math.max(targetZoom || 16, fleetZoom >= 16 ? fleetZoom : 16);
    fleetPanX = ((col + 0.5) / canvasCols) - 0.5 / fleetZoom;
    fleetPanY = ((row + 0.5) / canvasRows) - 0.5 / fleetZoom;
    clampFleetPan();
    highlightedActorIndex = idx1;
    highlightedActorUntil = Date.now() + 5500;
    if (el.fleetJumpInput) {
      el.fleetJumpInput.value = String(idx1);
    }
    syncFleetZoomToolbarUI();
    if (latestFleetCompact && latestFleetCompact.length > 256) {
      renderFleetCanvas(latestFleetCompact, total);
    }
  }

  function renderFleetMinimap(compactStr, total, palette, isLight, startCol, endCol, startRow, endRow) {
    if (!el.fleetMinimapWrap || !el.fleetMinimap) return;
    if (fleetZoom <= 1.02) {
      el.fleetMinimapWrap.hidden = true;
      return;
    }
    el.fleetMinimapWrap.hidden = false;
    if (el.minimapCoords) {
      const zStr = (fleetZoom < 10 ? fleetZoom.toFixed(1).replace(/\.0$/, '') : String(Math.round(fleetZoom))) + '×';
      el.minimapCoords.textContent = zStr + ' · R' + (startRow + 1) + '–' + (endRow + 1) + ' C' + (startCol + 1) + '–' + (endCol + 1);
    }

    const mCanvas = el.fleetMinimap;
    const mw = mCanvas.width || 256;
    const mh = mCanvas.height || 64;
    const mCtx = mCanvas.getContext('2d');
    if (!mCtx) return;

    mCtx.fillStyle = isLight ? '#f1f5f9' : '#0a0e17';
    mCtx.fillRect(0, 0, mw, mh);

    // Sample 2x2 blocks from 512x128 -> 256x64 minimap pixels
    const stepC = Math.max(1, Math.floor(canvasCols / mw));
    const stepR = Math.max(1, Math.floor(canvasRows / mh));
    for (let mr = 0; mr < mh; mr++) {
      const r = Math.min(canvasRows - 1, mr * stepR);
      const rowOffset = r * canvasCols;
      for (let mc = 0; mc < mw; mc++) {
        const c = Math.min(canvasCols - 1, mc * stepC);
        const idx = rowOffset + c;
        if (idx >= total || idx >= compactStr.length) continue;
        const code = (compactStr.charCodeAt(idx) - 64) & 15;
        if (code === 0) continue;
        mCtx.fillStyle = palette[code] || palette[9];
        mCtx.fillRect(mc, mr, 1, 1);
      }
    }

    // Draw current viewport box
    const vx = Math.floor(fleetPanX * mw);
    const vy = Math.floor(fleetPanY * mh);
    const vw = Math.max(4, Math.ceil((1 / fleetZoom) * mw));
    const vh = Math.max(3, Math.ceil((1 / fleetZoom) * mh));
    mCtx.strokeStyle = isLight ? '#0f172a' : '#00ffcc';
    mCtx.lineWidth = 1.5;
    mCtx.strokeRect(vx + 0.5, vy + 0.5, Math.min(mw - vx - 1, vw), Math.min(mh - vy - 1, vh));
    mCtx.fillStyle = isLight ? 'rgba(2, 132, 199, 0.16)' : 'rgba(0, 255, 204, 0.16)';
    mCtx.fillRect(vx, vy, Math.min(mw - vx, vw), Math.min(mh - vy, vh));
  }

  function renderFleetMagnifierLoupe(ctx, compactStr, total, palette, isLight, dpr, targetW, targetH) {
    if (!fleetLoupeEnabled || fleetZoom >= 3.5) return;
    if (hoveredCanvasIndex < 1 || hoveredCanvasIndex > total) return;
    if (lastCanvasMouseX < 0 || lastCanvasMouseY < 0) return;

    const radius = 4; // 9x9 neighborhood
    const gridN = radius * 2 + 1;
    const cellPx = Math.round(13 * dpr);
    const headerH = Math.round(18 * dpr);
    const pad = Math.round(5 * dpr);
    const boxW = gridN * cellPx + pad * 2;
    const boxH = gridN * cellPx + pad * 2 + headerH;

    // Position loupe to the left/top of cursor so it never collides with hover tooltip on bottom-right
    const cursorX = lastCanvasMouseX * dpr;
    const cursorY = lastCanvasMouseY * dpr;
    let lx = cursorX - boxW - Math.round(16 * dpr);
    let ly = cursorY - Math.round(boxH * 0.5);
    if (lx < 8 * dpr) {
      lx = Math.min(targetW - boxW - 8 * dpr, cursorX + Math.round(18 * dpr));
      ly = cursorY - boxH - Math.round(14 * dpr);
    }
    if (ly < 8 * dpr) ly = 8 * dpr;
    if (ly + boxH > targetH - 8 * dpr) ly = Math.max(8 * dpr, targetH - boxH - 8 * dpr);

    ctx.save();
    ctx.fillStyle = isLight ? 'rgba(255, 255, 255, 0.96)' : 'rgba(10, 14, 23, 0.94)';
    ctx.strokeStyle = isLight ? '#0284c7' : '#00ffcc';
    ctx.lineWidth = Math.max(1.5, 1.6 * dpr);
    ctx.fillRect(lx, ly, boxW, boxH);
    ctx.strokeRect(lx, ly, boxW, boxH);

    // Header label
    ctx.font = '800 ' + Math.round(9 * dpr) + 'px "JetBrains Mono", monospace';
    ctx.fillStyle = isLight ? '#0369a1' : '#00ffcc';
    ctx.textBaseline = 'middle';
    ctx.fillText('10× LOUPE · #' + hoveredCanvasIndex, lx + pad, ly + Math.round(headerH * 0.55));

    const centerIdx0 = hoveredCanvasIndex - 1;
    const centerR = Math.floor(centerIdx0 / canvasCols);
    const centerC = centerIdx0 % canvasCols;
    const gridTop = ly + headerH;

    for (let dr = -radius; dr <= radius; dr++) {
      const r = centerR + dr;
      for (let dc = -radius; dc <= radius; dc++) {
        const c = centerC + dc;
        const gx = lx + pad + (dc + radius) * cellPx;
        const gy = gridTop + (dr + radius) * cellPx;
        const innerW = cellPx - Math.max(1, Math.round(1.2 * dpr));

        if (r < 0 || r >= canvasRows || c < 0 || c >= canvasCols) {
          ctx.fillStyle = isLight ? '#e2e8f0' : '#0f172a';
          ctx.fillRect(gx, gy, innerW, innerW);
          continue;
        }
        const idx0 = r * canvasCols + c;
        if (idx0 >= total || idx0 >= compactStr.length) continue;
        const idx1 = idx0 + 1;
        const raw = compactStr.charCodeAt(idx0) - 64;
        const code = raw & 15;
        const leased = (raw & 16) !== 0;

        ctx.fillStyle = palette[code] || palette[9];
        ctx.fillRect(gx, gy, innerW, innerW);

        const actorId = 'attendee-' + String(idx1).padStart(2, '0');
        const onPod = !!seatedActorPodMap[actorId] || code === 2 || code === 3 || code === 4 || code === 5 || code === 7;
        if (onPod) {
          const core = Math.max(2, Math.round(3.2 * dpr));
          ctx.fillStyle = isLight ? '#0f172a' : '#ffffff';
          ctx.fillRect(gx + Math.floor((innerW - core) / 2), gy + Math.floor((innerW - core) / 2), core, core);
        }
        if (idx1 % 16 === 0) {
          const pip = Math.max(2, Math.round(2.5 * dpr));
          ctx.fillStyle = leased ? '#fbbf24' : (isLight ? '#0284c7' : '#00ffcc');
          ctx.fillRect(gx + innerW - pip, gy, pip, pip);
        }
        if (dr === 0 && dc === 0) {
          ctx.strokeStyle = isLight ? '#0f172a' : '#ffffff';
          ctx.lineWidth = Math.max(1.5, 1.8 * dpr);
          ctx.strokeRect(gx - 0.5, gy - 0.5, innerW + 1, innerW + 1);
        }
      }
    }
    ctx.restore();
  }

  function renderFleetCanvas(compactStr, totalActors) {
    if (!el.fleetCanvas || !compactStr) return;
    const total = totalActors > 0 ? totalActors : compactStr.length;
    const dims = computeCanvasDims(total);
    canvasCols = dims.cols;
    canvasRows = dims.rows;
    clampFleetPan();

    const rect = el.fleetCanvas.getBoundingClientRect();
    const cssW = Math.max(320, Math.floor(rect.width || 1024));
    const cssH = Math.max(140, Math.floor(rect.height || 280));
    const dpr = Math.min(2, window.devicePixelRatio || 1);
    const targetW = Math.floor(cssW * dpr);
    const targetH = Math.floor(cssH * dpr);
    if (el.fleetCanvas.width !== targetW || el.fleetCanvas.height !== targetH) {
      el.fleetCanvas.width = targetW;
      el.fleetCanvas.height = targetH;
    }

    const ctx = el.fleetCanvas.getContext('2d');
    if (!ctx) return;
    const isLight = window.ActorView.getTheme && window.ActorView.getTheme() === 'light';
    const palette = isLight ? CANVAS_COLORS_LIGHT : CANVAS_COLORS_DARK;

    ctx.clearRect(0, 0, targetW, targetH);

    // Fast O(N) pass for global counters shown in subtitle
    let snapshottedCount = 0;
    let activeCount = 0;
    let leasedCount = 0;
    const len = Math.min(total, compactStr.length);
    for (let i = 0; i < len; i++) {
      const raw = compactStr.charCodeAt(i) - 64;
      const code = raw & 15;
      if (code === 1) snapshottedCount++;
      else if (code >= 2 && code <= 8) activeCount++;
      if ((raw & 16) !== 0) leasedCount++;
    }

    const worldW = targetW * fleetZoom;
    const worldH = targetH * fleetZoom;
    const offsetX = fleetPanX * worldW;
    const offsetY = fleetPanY * worldH;
    const cellW = worldW / canvasCols;
    const cellH = worldH / canvasRows;
    const cssCellW = cellW / dpr;
    const cssCellH = cellH / dpr;

    const startCol = Math.max(0, Math.floor(fleetPanX * canvasCols));
    const endCol = Math.min(canvasCols - 1, Math.ceil((fleetPanX + 1 / fleetZoom) * canvasCols));
    const startRow = Math.max(0, Math.floor(fleetPanY * canvasRows));
    const endRow = Math.min(canvasRows - 1, Math.ceil((fleetPanY + 1 / fleetZoom) * canvasRows));

    const isCardTier = cssCellW >= 26 && cssCellH >= 20;
    const isTileTier = !isCardTier && cssCellW >= 5.5;
    const gapX = isCardTier ? Math.max(2 * dpr, cellW * 0.08) : (cellW >= 3 ? Math.min(2.2 * dpr, Math.max(0.65 * dpr, cellW * 0.1)) : 0);
    const gapY = isCardTier ? Math.max(2 * dpr, cellH * 0.08) : (cellH >= 3 ? Math.min(2.2 * dpr, Math.max(0.65 * dpr, cellH * 0.1)) : 0);
    const dotW = Math.max(1, cellW - gapX);
    const dotH = Math.max(1, cellH - gapY);

    for (let r = startRow; r <= endRow; r++) {
      const rowBase = r * canvasCols;
      const y = r * cellH - offsetY;
      for (let c = startCol; c <= endCol; c++) {
        const i = rowBase + c;
        if (i >= len) break;
        const idx1 = i + 1;
        const raw = compactStr.charCodeAt(i) - 64;
        const code = raw & 15;
        const leased = (raw & 16) !== 0;
        const x = c * cellW - offsetX;
        const color = palette[code] || palette[9];

        if (isCardTier) {
          // Tier 3 (16x - 32x+): Inspector Mini-Cards with Actor #ID, State Label, Pod Name & QR Tag
          const actorId = 'attendee-' + String(idx1).padStart(2, '0');
          const seatInfo = seatedActorPodMap[actorId];
          const liveView = latestActorsMap[actorId];
          const podName = (seatInfo && seatInfo.podName) || (liveView && liveView.worker_pod) || '';
          const sPod = (seatInfo && seatInfo.shortPod) || (podName ? shortPodName(podName) : '');
          const onPod = !!podName || code === 2 || code === 3 || code === 4 || code === 5 || code === 7;
          const isQRSlot = (idx1 % 16 === 0);

          ctx.fillStyle = isLight
            ? (code === 0 ? '#f1f5f9' : '#ffffff')
            : (code === 0 ? '#0f172a' : '#131d31');
          ctx.fillRect(x, y, dotW, dotH);

          // Colored top accent bar
          const barH = Math.max(2.5 * dpr, Math.min(4.5 * dpr, dotH * 0.13));
          ctx.fillStyle = color;
          ctx.fillRect(x, y, dotW, barH);

          // Card border
          ctx.strokeStyle = leased ? '#fbbf24' : color;
          ctx.lineWidth = (onPod || leased) ? Math.max(1.5, 1.6 * dpr) : Math.max(1, 1.0 * dpr);
          ctx.strokeRect(x + 0.5, y + 0.5, dotW - 1, dotH - 1);

          // Text labels inside card
          ctx.save();
          ctx.beginPath();
          ctx.rect(x + 1, y + 1, Math.max(1, dotW - 2), Math.max(1, dotH - 2));
          ctx.clip();
          ctx.textBaseline = 'top';

          const idFontPx = Math.round(Math.max(8, Math.min(12.5, cssCellW * 0.21)) * dpr);
          ctx.font = '800 ' + idFontPx + 'px "JetBrains Mono", monospace';
          ctx.fillStyle = isLight ? '#0f172a' : '#f8fafc';
          ctx.fillText('#' + idx1, x + 3.5 * dpr, y + barH + 2 * dpr);

          // Top-right QR / Live marker
          if (leased || isQRSlot) {
            const badgeColor = leased ? '#f59e0b' : (isLight ? '#0284c7' : '#00ffcc');
            if (cssCellW >= 48) {
              ctx.font = '800 ' + Math.round(7.5 * dpr) + 'px "JetBrains Mono", monospace';
              ctx.fillStyle = badgeColor;
              const bTxt = leased ? 'LIVE' : 'QR';
              const tw = ctx.measureText(bTxt).width;
              ctx.fillText(bTxt, x + dotW - tw - 3.5 * dpr, y + barH + 2.5 * dpr);
            } else {
              const pip = Math.max(3, Math.round(4 * dpr));
              ctx.fillStyle = badgeColor;
              ctx.fillRect(x + dotW - pip - 2 * dpr, y + barH + 2 * dpr, pip, pip);
            }
          }

          // Middle row: State / Activity label
          const stateFontPx = Math.round(Math.max(7.2, Math.min(10.5, cssCellW * 0.17)) * dpr);
          ctx.font = '800 ' + stateFontPx + 'px "JetBrains Mono", monospace';
          ctx.fillStyle = color;
          const stLabel = cssCellW >= 54 ? (STATE_FULL_LABELS[code] || 'ACTIVE') : (STATE_SHORT_LABELS[code] || 'ACT');
          ctx.fillText(stLabel, x + 3.5 * dpr, y + barH + idFontPx + 4 * dpr);

          // Bottom row: Assigned Worker Pod or GCS / QR status (when height permits)
          if (cssCellH >= 34) {
            const metaFontPx = Math.round(Math.max(6.5, Math.min(9.5, cssCellW * 0.15)) * dpr);
            ctx.font = '700 ' + metaFontPx + 'px "JetBrains Mono", monospace';
            const metaY = y + dotH - metaFontPx - 3 * dpr;
            if (sPod) {
              ctx.fillStyle = isLight ? '#0369a1' : '#38bdf8';
              ctx.fillText(sPod, x + 3.5 * dpr, metaY);
            } else if (code === 6 || code === 1) {
              ctx.fillStyle = isLight ? '#64748b' : '#94a3b8';
              ctx.fillText(cssCellW >= 52 ? 'gcs://snap' : 'GCS', x + 3.5 * dpr, metaY);
            } else if (isQRSlot) {
              ctx.fillStyle = isLight ? '#0284c7' : '#00ffcc';
              ctx.fillText('QR 16x', x + 3.5 * dpr, metaY);
            }
          }
          ctx.restore();
        } else if (isTileTier) {
          // Tier 2 (4x - 12x): Tactical Cohort Tiles with Physical Pod Seat core & QR corner markers
          ctx.fillStyle = color;
          ctx.fillRect(x, y, dotW, dotH);

          const actorId = 'attendee-' + String(idx1).padStart(2, '0');
          const onPod = !!seatedActorPodMap[actorId] || code === 2 || code === 3 || code === 4 || code === 5 || code === 7;
          if (onPod) {
            const core = Math.max(2 * dpr, Math.min(5 * dpr, Math.min(dotW, dotH) * 0.34));
            ctx.fillStyle = isLight ? '#0f172a' : '#ffffff';
            ctx.fillRect(x + (dotW - core) * 0.5, y + (dotH - core) * 0.5, core, core);
          }
          if (idx1 % 16 === 0) {
            const pip = Math.max(2 * dpr, Math.min(4 * dpr, Math.min(dotW, dotH) * 0.28));
            ctx.fillStyle = leased ? '#fbbf24' : (isLight ? '#0f172a' : '#00ffcc');
            ctx.fillRect(x + dotW - pip, y, pip, pip);
          }
          if (leased) {
            ctx.strokeStyle = isLight ? '#0f172a' : '#ffffff';
            ctx.lineWidth = Math.max(1.2, 1.4 * dpr);
            ctx.strokeRect(x - 0.5, y - 0.5, dotW + 1, dotH + 1);
          }
        } else {
          // Tier 1 (1x - 3x): High-density 65,536 Macro Heatmap
          ctx.fillStyle = color;
          ctx.fillRect(x, y, dotW, dotH);
          if (leased) {
            ctx.strokeStyle = isLight ? '#0f172a' : '#ffffff';
            ctx.lineWidth = Math.max(1, 1.2 * dpr);
            ctx.strokeRect(x - 0.5, y - 0.5, dotW + 1, dotH + 1);
          }
        }
      }
    }

    // Highlighted actor spotlight ring (from Quick Jump or "65k ⌖" button on a Pod seat)
    if (highlightedActorIndex >= 1 && highlightedActorIndex <= total && Date.now() < highlightedActorUntil) {
      const hIdx0 = highlightedActorIndex - 1;
      const hr = Math.floor(hIdx0 / canvasCols);
      const hc = hIdx0 % canvasCols;
      const hx = hc * cellW - offsetX;
      const hy = hr * cellH - offsetY;
      ctx.save();
      ctx.strokeStyle = '#00ffcc';
      ctx.lineWidth = Math.max(2.5, 2.8 * dpr);
      ctx.strokeRect(hx - 3 * dpr, hy - 3 * dpr, dotW + 6 * dpr, dotH + 6 * dpr);
      ctx.strokeStyle = isLight ? '#0f172a' : '#ffffff';
      ctx.lineWidth = Math.max(1.2, 1.5 * dpr);
      ctx.strokeRect(hx - 1 * dpr, hy - 1 * dpr, dotW + 2 * dpr, dotH + 2 * dpr);
      ctx.restore();
    }

    // Hovered cell outline
    if (hoveredCanvasIndex >= 1 && hoveredCanvasIndex <= total) {
      const idx0 = hoveredCanvasIndex - 1;
      const hr = Math.floor(idx0 / canvasCols);
      const hc = idx0 % canvasCols;
      const hx = hc * cellW - offsetX;
      const hy = hr * cellH - offsetY;
      ctx.strokeStyle = isLight ? '#0f172a' : '#00ffcc';
      ctx.lineWidth = Math.max(1.5, 2.0 * dpr);
      ctx.strokeRect(hx - 1, hy - 1, dotW + 2, dotH + 2);
    }

    // Floating 10x Magnifier Loupe when hovering at low zoom
    renderFleetMagnifierLoupe(ctx, compactStr, total, palette, isLight, dpr, targetW, targetH);

    // Corner Mini-Map Navigator when zoomed in
    renderFleetMinimap(compactStr, total, palette, isLight, startCol, endCol, startRow, endRow);

    if (el.fleetSub) {
      const visibleActors = (endCol - startCol + 1) * (endRow - startRow + 1);
      const zoomNote = fleetZoom > 1.02
        ? (' · viewing ' + visibleActors.toLocaleString() + ' at ' + (fleetZoom < 10 ? fleetZoom.toFixed(1).replace(/\.0$/, '') : Math.round(fleetZoom)) + '× (drag to pan)')
        : ' · scroll or click 4×/16×/32× to zoom';
      const leaseNote = leasedCount > 0 ? (' · ' + leasedCount + ' live QR lease' + (leasedCount === 1 ? '' : 's')) : '';
      el.fleetSub.textContent =
        total.toLocaleString() + ' actors (' + canvasCols + '×' + canvasRows + ') · ' +
        activeCount.toLocaleString() + ' active · ' +
        snapshottedCount.toLocaleString() + ' suspended' + leaseNote + zoomNote;
    }
  }

  // renderFleetGrid draws one badge per actor the gateway reports, in ID
  // order, and drops badges for actors it no longer reports. When the gateway
  // sends fleet_compact (>256 actors), it renders the high-density canvas matrix.
  function renderFleetGrid(actorsMap) {
    if (!el.fleetGrid) return;
    if (latestFleetCompact && latestFleetCompact.length > 256) {
      el.fleetGrid.hidden = true;
      if (el.fleetCanvasWrap) {
        el.fleetCanvasWrap.hidden = false;
      }
      if (el.fleetZoomToolbar) {
        el.fleetZoomToolbar.hidden = false;
      }
      syncFleetZoomToolbarUI();
      renderFleetCanvas(latestFleetCompact, latestActorCount || latestFleetCompact.length);
      return;
    }

    el.fleetGrid.hidden = false;
    if (el.fleetCanvasWrap) {
      el.fleetCanvasWrap.hidden = true;
    }
    if (el.fleetZoomToolbar) {
      el.fleetZoomToolbar.hidden = true;
    }
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
      el.spotlightWorker.textContent = data.worker_pod ? (data.worker_pod + ' ↗') : 'None';
      el.spotlightWorker.className = 'drawer-metric-value' + (data.worker_pod ? ' highlight-cyan' : '');
      if (data.worker_pod) {
        el.spotlightWorker.dataset.pod = data.worker_pod;
        el.spotlightWorker.style.cursor = 'pointer';
        el.spotlightWorker.title = 'Click to zoom into ' + data.worker_pod + ' in Worker Pool Hardware Execution';
      } else {
        delete el.spotlightWorker.dataset.pod;
        el.spotlightWorker.style.cursor = '';
        el.spotlightWorker.removeAttribute('title');
      }
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

    // Agent volume (/workspace)
    if (el.spotlightVolume) {
      const vol = window.ActorView.volume(data);
      const sep = vol.bound ? ' → ' : ' · ';
      el.spotlightVolume.textContent = vol.sub ? vol.label + sep + vol.sub : vol.label;
      el.spotlightVolume.className = 'drawer-metric-value snapshot-path' + (vol.bound ? ' highlight-cyan' : '');
      if (vol.uri) {
        el.spotlightVolume.title = vol.uri;
      } else {
        el.spotlightVolume.removeAttribute('title');
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

  function setSimButtonLabel(btn, count) {
    if (!btn) return;
    const label = 'Simulate ' + count.toLocaleString();
    const textNodes = Array.from(btn.childNodes).filter((n) => n.nodeType === Node.TEXT_NODE);
    if (textNodes.length > 0) {
      textNodes[textNodes.length - 1].nodeValue = ' ' + label;
    } else {
      btn.textContent = label;
    }
  }

  function updateSimulationTargets(actorCount, seatsTotal) {
    if (actorCount > 100) {
      simTargetLow = seatsTotal > 0 ? Math.min(actorCount, seatsTotal) : Math.max(256, Math.round(actorCount / 16));
      simTargetHigh = actorCount;
    } else {
      simTargetLow = 25;
      simTargetHigh = 50;
    }
    setSimButtonLabel(el.btnSim25, simTargetLow);
    setSimButtonLabel(el.btnSim50, simTargetHigh);
    updateSimulationUI();
  }

  function updateSimulationUI() {
    if (simulationActive) {
      if (el.btnSimStop) el.btnSimStop.style.display = 'inline-flex';
      if (el.btnSim25) {
        if (simulationCount === simTargetLow) {
          el.btnSim25.classList.add('active');
        } else {
          el.btnSim25.classList.remove('active');
        }
      }
      if (el.btnSim50) {
        if (simulationCount === simTargetHigh) {
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
      if (count === simTargetLow && el.btnSim25) el.btnSim25.classList.add('active');
      if (count === simTargetHigh && el.btnSim50) el.btnSim50.classList.add('active');
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
          <span>Target: <code>${escapeHTML(currentAtespace)}/${escapeHTML(attendeeId)}</code></span>
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

  function positionHoverTooltip(clientX, clientY) {
    if (!el.hoverTooltip || el.hoverTooltip.hidden) return;
    const pad = 14;
    const vw = window.innerWidth || 1280;
    const vh = window.innerHeight || 720;
    const rect = el.hoverTooltip.getBoundingClientRect();
    let x = clientX + pad;
    let y = clientY + pad;
    if (x + rect.width + 12 > vw) {
      x = Math.max(8, clientX - rect.width - pad);
    }
    if (y + rect.height + 12 > vh) {
      y = Math.max(8, clientY - rect.height - pad);
    }
    el.hoverTooltip.style.left = x + 'px';
    el.hoverTooltip.style.top = y + 'px';
  }

  function showHoverTooltip(opts, clientX, clientY) {
    if (!el.hoverTooltip) return;
    el.hoverTipDot.className = 'hover-tip-dot ' + (opts.cls || '');
    el.hoverTipTitle.textContent = opts.title || '';
    if (opts.tag) {
      el.hoverTipTag.hidden = false;
      el.hoverTipTag.textContent = opts.tag;
    } else {
      el.hoverTipTag.hidden = true;
      el.hoverTipTag.textContent = '';
    }
    el.hoverTipState.textContent = opts.state || '';
    el.hoverTipMeta.textContent = opts.meta || '';
    if (Array.isArray(opts.items) && opts.items.length > 0) {
      el.hoverTipList.hidden = false;
      el.hoverTipList.replaceChildren();
      const maxItems = Math.min(opts.items.length, 6);
      for (let i = 0; i < maxItems; i++) {
        const row = document.createElement('div');
        row.textContent = opts.items[i];
        el.hoverTipList.appendChild(row);
      }
      if (opts.items.length > maxItems) {
        const more = document.createElement('div');
        more.textContent = '+' + (opts.items.length - maxItems) + ' more active seats';
        el.hoverTipList.appendChild(more);
      }
    } else {
      el.hoverTipList.hidden = true;
      el.hoverTipList.replaceChildren();
    }
    el.hoverTooltip.hidden = false;
    positionHoverTooltip(clientX, clientY);
  }

  function hideHoverTooltip() {
    if (el.hoverTooltip) {
      el.hoverTooltip.hidden = true;
    }
    lastCanvasMouseX = -1;
    lastCanvasMouseY = -1;
    if (hoveredCanvasIndex !== -1) {
      hoveredCanvasIndex = -1;
      if (latestFleetCompact && latestFleetCompact.length > 256) {
        renderFleetCanvas(latestFleetCompact, latestActorCount || latestFleetCompact.length);
      }
    }
  }

  function actorIndexFromCanvasEvent(e) {
    if (!el.fleetCanvas || !latestFleetCompact) return -1;
    const rect = el.fleetCanvas.getBoundingClientRect();
    if (!rect.width || !rect.height) return -1;
    const relX = Math.max(0, Math.min(rect.width - 0.01, e.clientX - rect.left));
    const relY = Math.max(0, Math.min(rect.height - 0.01, e.clientY - rect.top));
    lastCanvasMouseX = relX;
    lastCanvasMouseY = relY;
    const uvX = relX / rect.width;
    const uvY = relY / rect.height;
    const worldUVX = fleetPanX + uvX / fleetZoom;
    const worldUVY = fleetPanY + uvY / fleetZoom;
    if (worldUVX < 0 || worldUVX >= 1 || worldUVY < 0 || worldUVY >= 1) return -1;
    const col = Math.floor(worldUVX * canvasCols);
    const row = Math.floor(worldUVY * canvasRows);
    const idx = row * canvasCols + col + 1;
    const total = latestActorCount || latestFleetCompact.length;
    if (idx < 1 || idx > total) return -1;
    return idx;
  }

  function panFleetMinimapFromEvent(e) {
    if (!el.fleetMinimap || fleetZoom <= 1.01) return;
    const rect = el.fleetMinimap.getBoundingClientRect();
    if (!rect.width || !rect.height) return;
    const uvX = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
    const uvY = Math.max(0, Math.min(1, (e.clientY - rect.top) / rect.height));
    fleetPanX = uvX - 0.5 / fleetZoom;
    fleetPanY = uvY - 0.5 / fleetZoom;
    clampFleetPan();
    if (latestFleetCompact && latestFleetCompact.length > 256) {
      renderFleetCanvas(latestFleetCompact, latestActorCount || latestFleetCompact.length);
    }
  }

  function triggerFleetJumpInput() {
    if (!el.fleetJumpInput) return;
    const raw = (el.fleetJumpInput.value || '').trim().replace(/^#/, '');
    const num = parseInt(raw, 10);
    const total = latestActorCount || (latestFleetCompact ? latestFleetCompact.length : 65536);
    if (!num || num < 1 || num > total) {
      el.fleetJumpInput.focus();
      return;
    }
    focusActorOnFleetCanvas(num, 16);
  }

  function initControls() {
    if (el.btnSim25) {
      el.btnSim25.addEventListener('click', function() {
        startSimulation(simTargetLow);
      });
    }
    if (el.btnSim50) {
      el.btnSim50.addEventListener('click', function() {
        startSimulation(simTargetHigh);
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
    if (el.spotlightWorker) {
      el.spotlightWorker.addEventListener('click', function() {
        const podName = el.spotlightWorker.dataset.pod;
        if (podName) {
          closeSpotlightDrawer();
          zoomToTopologyPod(podName);
        }
      });
    }
    document.addEventListener('keydown', function(e) {
      if (e.key === 'Escape' || e.key === 'Esc') {
        if (el.spotlightDrawer && el.spotlightDrawer.classList.contains('open')) {
          closeSpotlightDrawer();
        } else if (topoSelectedPod) {
          if (topoSelectedNode && latestNodeGroups.length > 0) {
            zoomToTopologyNode(topoSelectedNode);
          } else {
            resetTopologyZoom();
          }
        } else if (topoSelectedNode) {
          resetTopologyZoom();
        } else if (fleetZoom > 1.01) {
          setFleetZoom(1, 0.5, 0.5);
        }
        if (el.githubRepoLink && document.activeElement === el.githubRepoLink) {
          el.githubRepoLink.blur();
        }
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

    // Hardware Topology Breadcrumb Navigation (All 16 Nodes -> Node -> Pod)
    if (el.workerTopoNav) {
      el.workerTopoNav.addEventListener('click', function(e) {
        const btn = e.target.closest('[data-topo-action]');
        if (!btn) return;
        const action = btn.dataset.topoAction;
        if (action === 'all-nodes') {
          resetTopologyZoom();
        } else if (action === 'zoom-node') {
          zoomToTopologyNode(btn.dataset.node || topoSelectedNode);
        } else if (action === 'prev-node') {
          stepTopologyNode(-1);
        } else if (action === 'next-node') {
          stepTopologyNode(1);
        } else if (action === 'prev-pod') {
          stepTopologyPod(-1);
        } else if (action === 'next-pod') {
          stepTopologyPod(1);
        }
      });
    }

    // Hardware Topology Grid Hover & Drill-Down Clicks (Node -> Pod -> Actor)
    if (el.workerList) {
      el.workerList.addEventListener('mousemove', function(e) {
        const target = e.target.closest('[data-hover-kind]');
        if (!target) {
          hideHoverTooltip();
          return;
        }
        const kind = target.dataset.hoverKind;
        const podName = target.dataset.pod || '';
        const nodeName = target.dataset.node || '';
        if (kind === 'pod' && podName) {
          const info = podHoverDirectory[podName] || { podName: podName, nodeName: nodeName, actors: [], seats: latestSeatsPerWorker };
          const actors = info.actors || [];
          const cls = dominantPodClass(actors);
          const items = actors.map((wa) => {
            const b = actorBadgeForWorkerActor(wa);
            return formatActorName(wa.actor_id) + ' — ' + (b ? b.label : wa.state);
          });
          const nodeDesc = info.nodeLabel ? (info.nodeLabel + ' (' + info.nodeName + ')') : (info.nodeName || 'Worker Pool');
          showHoverTooltip({
            cls: cls,
            title: podName,
            tag: actors.length + '/' + info.seats + ' SEATS',
            state: actors.length > 0 ? (actors.length + ' active actor' + (actors.length === 1 ? '' : 's') + ' on pod · Click to zoom into node') : 'All 16 actor seats idle · Click to zoom into node',
            meta: 'GKE Node: ' + nodeDesc,
            items: items
          }, e.clientX, e.clientY);
        } else if (kind === 'seat') {
          const seatNum = target.dataset.seat || '1';
          const actorId = target.dataset.actor || '';
          const clickHint = topoSelectedPod
            ? ' · Click card for Actor Spotlight'
            : ' · Click to zoom into pod (' + shortPodName(podName) + ')';
          if (actorId) {
            const view = latestActorsMap[actorId] || { actor_id: actorId, state: 'RUNNING', worker_pod: podName };
            const b = window.ActorView.badge(view);
            showHoverTooltip({
              cls: b.cls,
              title: formatActorName(actorId) + ' (' + actorId + ')',
              tag: 'SEAT ' + seatNum + '/16',
              state: (b.sub ? (b.label + ' · ' + b.sub) : b.label) + clickHint,
              meta: 'Worker Pod: ' + podName
            }, e.clientX, e.clientY);
          } else {
            showHoverTooltip({
              cls: 'never-started',
              title: 'Seat ' + seatNum + ' of 16',
              tag: 'IDLE',
              state: 'Available execution seat' + (topoSelectedPod ? '' : clickHint),
              meta: 'Worker Pod: ' + podName
            }, e.clientX, e.clientY);
          }
        }
      });
      el.workerList.addEventListener('mouseleave', hideHoverTooltip);
      el.workerList.addEventListener('click', function(e) {
        // 1. "65k ⌖" button inside Level 2 Single Pod View: locate & zoom to actor in the 65k fleet matrix
        const locateBtn = e.target.closest('[data-locate-actor]');
        if (locateBtn) {
          e.stopPropagation();
          const actorId = locateBtn.dataset.locateActor;
          if (actorId) {
            switchViewMode('grid');
            focusActorOnFleetCanvas(actorId, 16);
          }
          return;
        }

        // 2. Explicit Node corner zoom button or Node header click -> Level 1 (Single Node View: 16 pods with slots)
        const zoomNodeTrigger = e.target.closest('[data-zoom-node]');
        if (zoomNodeTrigger) {
          e.stopPropagation();
          zoomToTopologyNode(zoomNodeTrigger.dataset.zoomNode);
          return;
        }

        // 3. Explicit Pod corner zoom button or Pod header click -> Level 2 (Single Pod View: 16 actors in pod)
        const zoomPodTrigger = e.target.closest('[data-zoom-pod]');
        if (zoomPodTrigger) {
          e.stopPropagation();
          zoomToTopologyPod(zoomPodTrigger.dataset.zoomPod, zoomPodTrigger.dataset.node || topoSelectedNode);
          return;
        }

        // 4. In Level 2 (Single Pod View), clicking an occupied actor seat card opens Spotlight & highlights in 65k grid
        const podActorCard = e.target.closest('.pod-actor-seat-card');
        if (podActorCard) {
          const actorId = podActorCard.dataset.actor;
          if (actorId) {
            focusActorOnFleetCanvas(actorId, Math.max(fleetZoom, 16));
            openSpotlightDrawer(actorId);
          }
          return;
        }

        // 5. Clicking a cell inside a 4x4 grid:
        //    - If it's a pod cell inside Level 0 (All 16 Nodes), zoom into that Node (Level 1) so all 16 pods with their slots take the entire space!
        //    - If it's a seat cell inside Level 1 (Single Node View), zoom into that Pod (Level 2) so all 16 actors in that pod take the entire space!
        const cell = e.target.closest('.seat-cell');
        if (cell) {
          const kind = cell.dataset.hoverKind;
          if (kind === 'pod' && cell.dataset.node) {
            zoomToTopologyNode(cell.dataset.node, cell.dataset.pod || null);
            return;
          }
          if (kind === 'seat' && cell.dataset.pod && !topoSelectedPod) {
            zoomToTopologyPod(cell.dataset.pod, cell.dataset.node || topoSelectedNode);
            return;
          }
          if (cell.dataset.actor) {
            openSpotlightDrawer(cell.dataset.actor);
          }
        }
      });
    }

    // 65,536 Fleet Matrix Zoom Toolbar Controls
    if (el.fleetZoomPresets) {
      el.fleetZoomPresets.forEach((btn) => {
        btn.addEventListener('click', function() {
          const target = parseFloat(btn.dataset.zoom || '1');
          setFleetZoom(target, 0.5, 0.5);
        });
      });
    }
    if (el.btnFleetZoomIn) {
      el.btnFleetZoomIn.addEventListener('click', function() {
        const next = fleetZoom < 2 ? 2 : (fleetZoom < 4 ? 4 : (fleetZoom < 8 ? 8 : (fleetZoom < 16 ? 16 : 32)));
        setFleetZoom(next, 0.5, 0.5);
      });
    }
    if (el.btnFleetZoomOut) {
      el.btnFleetZoomOut.addEventListener('click', function() {
        const prev = fleetZoom > 16 ? 16 : (fleetZoom > 8 ? 8 : (fleetZoom > 4 ? 4 : (fleetZoom > 2 ? 2 : 1)));
        setFleetZoom(prev, 0.5, 0.5);
      });
    }
    if (el.btnFleetZoomReset) {
      el.btnFleetZoomReset.addEventListener('click', function() {
        setFleetZoom(1, 0.5, 0.5);
      });
    }
    if (el.btnMinimapReset) {
      el.btnMinimapReset.addEventListener('click', function(e) {
        e.stopPropagation();
        setFleetZoom(1, 0.5, 0.5);
      });
    }
    if (el.btnFleetLoupe) {
      el.btnFleetLoupe.addEventListener('click', function() {
        fleetLoupeEnabled = !fleetLoupeEnabled;
        syncFleetZoomToolbarUI();
        if (latestFleetCompact && latestFleetCompact.length > 256) {
          renderFleetCanvas(latestFleetCompact, latestActorCount || latestFleetCompact.length);
        }
      });
    }
    if (el.btnFleetJump) {
      el.btnFleetJump.addEventListener('click', triggerFleetJumpInput);
    }
    if (el.fleetJumpInput) {
      el.fleetJumpInput.addEventListener('keydown', function(e) {
        if (e.key === 'Enter') {
          e.preventDefault();
          triggerFleetJumpInput();
        }
      });
    }

    // Minimap Click & Drag Panning
    if (el.fleetMinimap) {
      el.fleetMinimap.addEventListener('mousedown', function(e) {
        if (e.button !== 0) return;
        e.preventDefault();
        e.stopPropagation();
        minimapIsDragging = true;
        panFleetMinimapFromEvent(e);
      });
    }
    window.addEventListener('mousemove', function(e) {
      if (minimapIsDragging) {
        panFleetMinimapFromEvent(e);
      }
      if (fleetIsDragging && el.fleetCanvas) {
        const rect = el.fleetCanvas.getBoundingClientRect();
        if (!rect.width || !rect.height) return;
        const dx = e.clientX - fleetDragStartX;
        const dy = e.clientY - fleetDragStartY;
        if (Math.abs(dx) > 4 || Math.abs(dy) > 4) {
          fleetDragMoved = true;
        }
        if (fleetDragMoved && fleetZoom > 1.01) {
          fleetPanX = fleetDragStartPanX - (dx / rect.width) / fleetZoom;
          fleetPanY = fleetDragStartPanY - (dy / rect.height) / fleetZoom;
          clampFleetPan();
          if (latestFleetCompact && latestFleetCompact.length > 256) {
            renderFleetCanvas(latestFleetCompact, latestActorCount || latestFleetCompact.length);
          }
        }
      }
    });
    window.addEventListener('mouseup', function() {
      minimapIsDragging = false;
      if (fleetIsDragging) {
        fleetIsDragging = false;
        if (el.fleetCanvas) {
          el.fleetCanvas.classList.remove('is-dragging');
        }
      }
    });

    if (el.fleetCanvas) {
      // Wheel / Trackpad Pinch Zoom centered on cursor
      el.fleetCanvas.addEventListener('wheel', function(e) {
        if (!latestFleetCompact || latestFleetCompact.length <= 256) return;
        e.preventDefault();
        const rect = el.fleetCanvas.getBoundingClientRect();
        if (!rect.width || !rect.height) return;
        const uvX = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
        const uvY = Math.max(0, Math.min(1, (e.clientY - rect.top) / rect.height));
        const factor = e.deltaY < 0 ? 1.28 : (1 / 1.28);
        const nextZoom = Math.max(1, Math.min(32, fleetZoom * factor));
        setFleetZoom(nextZoom, uvX, uvY);
      }, { passive: false });

      // Drag-to-pan when zoomed in
      el.fleetCanvas.addEventListener('mousedown', function(e) {
        if (e.button !== 0) return;
        fleetIsDragging = true;
        fleetDragMoved = false;
        fleetDragStartX = e.clientX;
        fleetDragStartY = e.clientY;
        fleetDragStartPanX = fleetPanX;
        fleetDragStartPanY = fleetPanY;
        if (fleetZoom > 1.01) {
          el.fleetCanvas.classList.add('is-dragging');
        }
      });

      // Double-click to zoom in centered on clicked actor neighborhood
      el.fleetCanvas.addEventListener('dblclick', function(e) {
        if (!latestFleetCompact || latestFleetCompact.length <= 256) return;
        e.preventDefault();
        const rect = el.fleetCanvas.getBoundingClientRect();
        if (!rect.width || !rect.height) return;
        const uvX = Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width));
        const uvY = Math.max(0, Math.min(1, (e.clientY - rect.top) / rect.height));
        const nextZoom = fleetZoom < 3.5 ? 4 : (fleetZoom < 14 ? 16 : (fleetZoom < 28 ? 32 : 1));
        setFleetZoom(nextZoom, uvX, uvY);
      });

      el.fleetCanvas.addEventListener('mousemove', function(e) {
        if (fleetIsDragging && fleetDragMoved) {
          if (el.hoverTooltip) el.hoverTooltip.hidden = true;
          return;
        }
        const idx = actorIndexFromCanvasEvent(e);
        if (idx < 1) {
          hideHoverTooltip();
          return;
        }
        const needsRedraw = (hoveredCanvasIndex !== idx) || (fleetLoupeEnabled && fleetZoom < 3.5);
        hoveredCanvasIndex = idx;
        if (needsRedraw) {
          renderFleetCanvas(latestFleetCompact, latestActorCount || latestFleetCompact.length);
        }
        const actorId = 'attendee-' + String(idx).padStart(2, '0');
        const decoded = window.ActorView.decodeCompactBadge(latestFleetCompact.charCodeAt(idx - 1));
        const liveView = latestActorsMap[actorId];
        const seatInfo = seatedActorPodMap[actorId];
        const b = liveView ? window.ActorView.badge(liveView) : decoded;
        const leased = (liveView && liveView.leased) || decoded.leased;
        const isReservable = (idx % 16 === 0);
        const tag = leased ? 'LIVE QR LEASE' : (isReservable ? 'QR SLOT (16x)' : ('#' + idx));
        const podName = (seatInfo && seatInfo.podName) || (liveView && liveView.worker_pod) || '';
        const nodeLabel = (seatInfo && seatInfo.nodeLabel) ? (' (' + seatInfo.nodeLabel + ')') : '';
        const podMeta = podName
          ? ('Seated on Pod: ' + podName + nodeLabel + (seatInfo ? (' · Seat ' + seatInfo.seatIndex + '/16') : ''))
          : (decoded.code === 6 || decoded.code === 1
              ? 'Checkpointed in GCS · 0 pod RAM consumed while idle'
              : (isReservable ? 'Reserved for live QR scan · click to inspect' : 'Click to open Actor Spotlight · Double-click or scroll to zoom'));
        showHoverTooltip({
          cls: b.cls,
          title: formatActorName(actorId) + ' (' + actorId + ')',
          tag: tag,
          state: b.sub ? (b.label + ' · ' + b.sub) : b.label,
          meta: podMeta
        }, e.clientX, e.clientY);
      });
      el.fleetCanvas.addEventListener('mouseleave', hideHoverTooltip);
      el.fleetCanvas.addEventListener('click', function(e) {
        if (fleetDragMoved) {
          fleetDragMoved = false;
          return;
        }
        const idx = actorIndexFromCanvasEvent(e);
        if (idx >= 1) {
          const actorId = 'attendee-' + String(idx).padStart(2, '0');
          openSpotlightDrawer(actorId);
        }
      });
      window.addEventListener('resize', function() {
        if (latestFleetCompact && latestFleetCompact.length > 256) {
          renderFleetCanvas(latestFleetCompact, latestActorCount || latestFleetCompact.length);
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
    initGitHubRepoQR();
    initControls();
    fetchSimulationStatus();
    fetchTelemetry();
    connectStageStream();
    fetchStreamFeedHistory();
    setInterval(fetchTelemetry, 500);
    setInterval(fetchSimulationStatus, 2000);
  });
})();
