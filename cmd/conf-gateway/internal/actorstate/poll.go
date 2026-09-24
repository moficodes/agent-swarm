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
	"time"
)

// Poll lists actors every interval until ctx ends, applying each result to
// b. observe, if set, receives each poll's latency and error.
func Poll(ctx context.Context, c ActorControl, interval time.Duration, b *Board, observe func(time.Duration, error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		start := time.Now()
		pollCtx, cancel := context.WithTimeout(ctx, max(interval*5, 5*time.Second))
		statuses, err := c.List(pollCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			b.SetUnavailable(err.Error())
		} else {
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
