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

package actorproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// approvals tracks the approval each attendee was last shown and the
// decision each attendee is forwarding.
type approvals struct {
	mu      sync.Mutex
	pending map[string]*types.ApprovalRequest
	// inFlight holds the approval ID each attendee is currently
	// forwarding, so a second copy of the same decision cannot run concurrently.
	inFlight map[string]string
}

func newApprovals() *approvals {
	return &approvals{
		pending:  make(map[string]*types.ApprovalRequest),
		inFlight: make(map[string]string),
	}
}

// SetPending records the approval the attendee's actor is waiting on.
func (a *approvals) SetPending(attendeeID string, req *types.ApprovalRequest) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending[attendeeID] = req
}

// ClearPending forgets the attendee's pending approval.
func (a *approvals) ClearPending(attendeeID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pending, attendeeID)
}

// PendingApproval returns a copy of the attendee's pending approval, or nil.
func (a *approvals) PendingApproval(attendeeID string) *types.ApprovalRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	if pa, ok := a.pending[attendeeID]; ok && pa != nil {
		copyPA := *pa
		return &copyPA
	}
	return nil
}

// ResetPending forgets every pending approval.
func (a *approvals) ResetPending() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pending = make(map[string]*types.ApprovalRequest)
}

// ClaimApproval binds an approval decision to the approval this attendee was
// last shown. On success it rewrites r.Body to carry the proposed code, never
// code supplied by the client, and marks the approval in flight until release
// is called. The pending approval itself stays until ClearPending, once the
// actor answers 200. On failure it writes the error response.
func (a *approvals) ClaimApproval(w http.ResponseWriter, r *http.Request, attendeeID string) (types.ApprovalResponse, func(), bool) {
	var req types.ApprovalResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json payload", http.StatusBadRequest)
		return req, nil, false
	}
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return req, nil, false
	}

	a.mu.Lock()
	pending, ok := a.pending[attendeeID]
	if !ok || pending == nil || pending.ApprovalID != req.ApprovalID {
		a.mu.Unlock()
		http.Error(w, "no matching pending approval", http.StatusConflict)
		return req, nil, false
	}
	if a.inFlight[attendeeID] == req.ApprovalID {
		a.mu.Unlock()
		http.Error(w, "approval already in progress", http.StatusConflict)
		return req, nil, false
	}
	a.inFlight[attendeeID] = req.ApprovalID
	req.Code = pending.Code
	req.Language = pending.Language
	a.mu.Unlock()

	release := func() {
		a.mu.Lock()
		if a.inFlight[attendeeID] == req.ApprovalID {
			delete(a.inFlight, attendeeID)
		}
		a.mu.Unlock()
	}

	body, _ := json.Marshal(req)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return req, release, true
}
