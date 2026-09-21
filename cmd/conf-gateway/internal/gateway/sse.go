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

package gateway

import (
	"sync"

	"github.com/agent-substrate/substrate/demos/conference/pkg/types"
)

// EventBroker maintains active SSE subscribers and dispatches lifecycle events.
type EventBroker struct {
	mu          sync.RWMutex
	subscribers map[string][]chan types.StateChangeEvent
}

// NewEventBroker initializes and returns a new EventBroker.
func NewEventBroker() *EventBroker {
	return &EventBroker{
		subscribers: make(map[string][]chan types.StateChangeEvent),
	}
}

// Subscribe creates and registers an event channel for the given attendee.
func (b *EventBroker) Subscribe(attendeeID string) chan types.StateChangeEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan types.StateChangeEvent, 16)
	b.subscribers[attendeeID] = append(b.subscribers[attendeeID], ch)
	return ch
}

// Unsubscribe deregisters and closes the event channel for the given attendee.
func (b *EventBroker) Unsubscribe(attendeeID string, ch chan types.StateChangeEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := b.subscribers[attendeeID]
	for i, sub := range subs {
		if sub == ch {
			b.subscribers[attendeeID] = append(subs[:i], subs[i+1:]...)
			close(ch)
			break
		}
	}
	if len(b.subscribers[attendeeID]) == 0 {
		delete(b.subscribers, attendeeID)
	}
}

// Publish broadcasts an event to all subscribers listening for the event's attendee.
func (b *EventBroker) Publish(event types.StateChangeEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers[event.AttendeeID] {
		select {
		case ch <- event:
		default:
		}
	}
}

// SubscriberCount returns the current number of active subscriber channels for an attendee.
func (b *EventBroker) SubscriberCount(attendeeID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers[attendeeID])
}
