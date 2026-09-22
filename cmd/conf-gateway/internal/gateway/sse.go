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
	mu                sync.RWMutex
	subscribers       map[string][]chan types.StateChangeEvent
	actionSubscribers []chan types.ActionLogEntry
	resetSubscribers  []chan struct{}
}

// NewEventBroker initializes and returns a new EventBroker.
func NewEventBroker() *EventBroker {
	return &EventBroker{
		subscribers:       make(map[string][]chan types.StateChangeEvent),
		actionSubscribers: make([]chan types.ActionLogEntry, 0),
		resetSubscribers:  make([]chan struct{}, 0),
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
			copy(subs[i:], subs[i+1:])
			subs[len(subs)-1] = nil
			b.subscribers[attendeeID] = subs[:len(subs)-1]
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

// SubscribeActions creates and registers an action event channel for global action stream events.
func (b *EventBroker) SubscribeActions() chan types.ActionLogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan types.ActionLogEntry, 64)
	b.actionSubscribers = append(b.actionSubscribers, ch)
	return ch
}

// UnsubscribeActions deregisters and closes the action log event channel.
func (b *EventBroker) UnsubscribeActions(ch chan types.ActionLogEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, sub := range b.actionSubscribers {
		if sub == ch {
			copy(b.actionSubscribers[i:], b.actionSubscribers[i+1:])
			b.actionSubscribers[len(b.actionSubscribers)-1] = nil
			b.actionSubscribers = b.actionSubscribers[:len(b.actionSubscribers)-1]
			close(ch)
			break
		}
	}
}

// PublishAction broadcasts an action entry to all active action stream subscribers.
func (b *EventBroker) PublishAction(entry types.ActionLogEntry) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.actionSubscribers {
		select {
		case ch <- entry:
		default:
		}
	}
}

// ActionSubscriberCount returns the current number of active action subscribers.
func (b *EventBroker) ActionSubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.actionSubscribers)
}

// SubscribeReset creates and registers a reset event channel.
func (b *EventBroker) SubscribeReset() chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan struct{}, 8)
	b.resetSubscribers = append(b.resetSubscribers, ch)
	return ch
}

// UnsubscribeReset deregisters and closes a reset event channel.
func (b *EventBroker) UnsubscribeReset(ch chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, sub := range b.resetSubscribers {
		if sub == ch {
			copy(b.resetSubscribers[i:], b.resetSubscribers[i+1:])
			b.resetSubscribers[len(b.resetSubscribers)-1] = nil
			b.resetSubscribers = b.resetSubscribers[:len(b.resetSubscribers)-1]
			close(ch)
			break
		}
	}
}

// PublishReset broadcasts a reset signal to all reset subscribers.
func (b *EventBroker) PublishReset() {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.resetSubscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
