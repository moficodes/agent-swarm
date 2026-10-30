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

package feed

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/agent-substrate/agent-swarm/pkg/types"
)

// Event is one server-sent event: its name and its JSON body.
type Event struct {
	Name string
	Data []byte
}

// Broker maintains active SSE subscribers and dispatches events to them.
type Broker struct {
	mu                sync.RWMutex
	subscribers       map[string][]chan Event
	actionSubscribers []chan types.ActionLogEntry
	resetSubscribers  []chan struct{}
}

// NewBroker initializes and returns a new Broker.
func NewBroker() *Broker {
	return &Broker{
		subscribers:       make(map[string][]chan Event),
		actionSubscribers: make([]chan types.ActionLogEntry, 0),
		resetSubscribers:  make([]chan struct{}, 0),
	}
}

// Subscribe creates and registers an event channel for the given attendee.
func (b *Broker) Subscribe(attendeeID string) chan Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan Event, 16)
	b.subscribers[attendeeID] = append(b.subscribers[attendeeID], ch)
	return ch
}

// Unsubscribe deregisters and closes the event channel for the given attendee.
func (b *Broker) Unsubscribe(attendeeID string, ch chan Event) {
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

// Publish sends an event to every subscriber of one attendee. A subscriber
// whose buffer is full misses the event.
func (b *Broker) Publish(attendeeID string, e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers[attendeeID] {
		select {
		case ch <- e:
		default:
		}
	}
}

// PublishAll sends an event to every attendee subscriber and signals every
// action stream subscriber that the stage was reset.
func (b *Broker) PublishAll(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, subs := range b.subscribers {
		for _, ch := range subs {
			select {
			case ch <- e:
			default:
			}
		}
	}
	for _, ch := range b.resetSubscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// SubscriberCount returns the current number of active subscriber channels for an attendee.
func (b *Broker) SubscriberCount(attendeeID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers[attendeeID])
}

// SubscribeActions creates and registers an action event channel for global action stream events.
func (b *Broker) SubscribeActions() chan types.ActionLogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan types.ActionLogEntry, 64)
	b.actionSubscribers = append(b.actionSubscribers, ch)
	return ch
}

// UnsubscribeActions deregisters and closes the action log event channel.
func (b *Broker) UnsubscribeActions(ch chan types.ActionLogEntry) {
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
func (b *Broker) PublishAction(entry types.ActionLogEntry) {
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
func (b *Broker) ActionSubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.actionSubscribers)
}

// SubscribeReset creates and registers a reset event channel.
func (b *Broker) SubscribeReset() chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan struct{}, 8)
	b.resetSubscribers = append(b.resetSubscribers, ch)
	return ch
}

// UnsubscribeReset deregisters and closes a reset event channel.
func (b *Broker) UnsubscribeReset(ch chan struct{}) {
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

// SendToAttendee sends one SSE event to every subscriber of an attendee.
func (b *Broker) SendToAttendee(attendee, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[gateway] encoding %s event: %v", event, err)
		return
	}
	b.Publish(attendee, Event{Name: event, Data: data})
}

// Broadcast sends one SSE event to every attendee subscriber and signals
// every action stream subscriber.
func (b *Broker) Broadcast(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[gateway] encoding %s event: %v", event, err)
		return
	}
	b.PublishAll(Event{Name: event, Data: data})
}

// ServeAttendee streams one attendee's events to w until the client goes
// away. The first event is an actor_view carrying initialView().
func (b *Broker) ServeAttendee(w http.ResponseWriter, r *http.Request, attendeeID string, initialView func() any) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Subscribe before the client sees 200 so no view published after that
	// is missed.
	ch := b.Subscribe(attendeeID)
	defer b.Unsubscribe(attendeeID, ch)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if initData, err := json.Marshal(initialView()); err == nil {
		if _, err := fmt.Fprintf(w, "event: actor_view\ndata: %s\n\n", initData); err != nil {
			return
		}
		flusher.Flush()
	}

	ctxDone := r.Context().Done()
	for {
		select {
		case <-ctxDone:
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Name, event.Data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// ServeActions streams action log entries and stage resets to w until the
// client goes away.
func (b *Broker) ServeActions(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Subscribe before the client sees 200 so no action or reset published
	// after that is missed.
	actionCh := b.SubscribeActions()
	defer b.UnsubscribeActions(actionCh)

	resetCh := b.SubscribeReset()
	defer b.UnsubscribeReset(resetCh)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctxDone := r.Context().Done()
	for {
		select {
		case <-ctxDone:
			return
		case entry, ok := <-actionCh:
			if !ok {
				return
			}
			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: action_entry\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		case _, ok := <-resetCh:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "event: reset\ndata: {}\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
