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

package actorstate

import (
	"context"
	"log"
	"time"
)

// unavailableReason is what attendees see when a poll fails. The error
// itself, which names internal endpoints and carries stderr, is only logged.
const unavailableReason = "ateapi unreachable"

// Poll lists actors every interval until ctx ends, applying each result to
// b. observe, if set, receives each poll's latency and error.
func Poll(ctx context.Context, c ActorControl, interval time.Duration, b *Board, observe func(time.Duration, error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var lastErr string
	for {
		start := time.Now()
		pollCtx, cancel := context.WithTimeout(ctx, max(interval*20, 45*time.Second))
		statuses, err := c.List(pollCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if err.Error() != lastErr {
				log.Printf("[gateway] listing actors: %v", err)
				lastErr = err.Error()
			}
			b.SetUnavailable(unavailableReason)
		} else {
			lastErr = ""
			b.SetStatuses(statuses)
		}
		if observe != nil {
			observe(time.Since(start), err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
