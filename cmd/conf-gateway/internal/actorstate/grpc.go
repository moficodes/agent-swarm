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
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-substrate/agent-swarm/pkg/types"
	ateapi "go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	authv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	ateSystemNamespace = "ate-system"
	ateClientSA        = "ate-client"
	ateAPIAudience     = "api.ate-system.svc"
	ateSignerName      = "servicedns.podcert.ate.dev/identity"
	canaryLabelKey     = "podcert.ate.dev/canarying"
	canaryLabelLive    = "live"
	controlConnPoolLen = 32
)

// GRPCControl implements ActorControl directly over gRPC to ate-api-server,
// caching the minted ate-client bearer token and reusing persistent HTTP/2
// connections instead of spawning a kubectl-ate subprocess per poll.
type GRPCControl struct {
	k8sClient kubernetes.Interface
	endpoint  string
	atespace  string
	caFile    string

	mu          sync.Mutex
	listConn    *grpc.ClientConn
	listClient  ateapi.ControlClient
	conns       []*grpc.ClientConn
	clients     []ateapi.ControlClient
	rrSeq       atomic.Uint64
	cachedToken string
	tokenExpiry time.Time
}

// NewGRPCControl returns a persistent gRPC ActorControl client.
func NewGRPCControl(k8sClient kubernetes.Interface, endpoint, atespace, caFile string) (*GRPCControl, error) {
	if k8sClient == nil {
		return nil, fmt.Errorf("k8sClient is required")
	}
	if endpoint == "" {
		endpoint = "api.ate-system.svc:443"
	}
	if atespace == "" {
		atespace = "default"
	}
	return &GRPCControl{
		k8sClient: k8sClient,
		endpoint:  endpoint,
		atespace:  atespace,
		caFile:    caFile,
	}, nil
}

// Close closes the underlying gRPC connections if open.
func (g *GRPCControl) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	var firstErr error
	if g.listConn != nil {
		if err := g.listConn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		g.listConn = nil
		g.listClient = nil
	}
	for _, conn := range g.conns {
		if conn != nil {
			if err := conn.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	g.conns = nil
	g.clients = nil
	return firstErr
}

func (g *GRPCControl) getToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	if g.cachedToken != "" && time.Now().Add(5*time.Minute).Before(g.tokenExpiry) {
		tok := g.cachedToken
		g.mu.Unlock()
		return tok, nil
	}
	g.mu.Unlock()

	expSec := int64(3600)
	tr, err := g.k8sClient.CoreV1().ServiceAccounts(ateSystemNamespace).CreateToken(
		ctx,
		ateClientSA,
		&authv1.TokenRequest{
			Spec: authv1.TokenRequestSpec{
				Audiences:         []string{ateAPIAudience},
				ExpirationSeconds: &expSec,
			},
		},
		metav1.CreateOptions{},
	)
	if err != nil {
		return "", fmt.Errorf("minting ate-client token: %w", err)
	}

	g.mu.Lock()
	g.cachedToken = tr.Status.Token
	if !tr.Status.ExpirationTimestamp.IsZero() {
		g.tokenExpiry = tr.Status.ExpirationTimestamp.Time
	} else {
		g.tokenExpiry = time.Now().Add(55 * time.Minute)
	}
	g.mu.Unlock()
	return tr.Status.Token, nil
}

func (g *GRPCControl) loadCertPool(ctx context.Context) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if g.caFile != "" {
		pemBytes, err := os.ReadFile(g.caFile)
		if err == nil && pool.AppendCertsFromPEM(pemBytes) {
			return pool, nil
		}
	}
	opts := metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.signerName=%s", ateSignerName),
		LabelSelector: fmt.Sprintf("%s=%s", canaryLabelKey, canaryLabelLive),
	}
	if v1Bundles, err := g.k8sClient.CertificatesV1().ClusterTrustBundles().List(ctx, opts); err == nil {
		for _, b := range v1Bundles.Items {
			pool.AppendCertsFromPEM([]byte(b.Spec.TrustBundle))
		}
		return pool, nil
	}
	bundles, err := g.k8sClient.CertificatesV1beta1().ClusterTrustBundles().List(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("listing ClusterTrustBundles: %w", err)
	}
	for _, b := range bundles.Items {
		pool.AppendCertsFromPEM([]byte(b.Spec.TrustBundle))
	}
	return pool, nil
}

type dynamicTokenAuth struct {
	control *GRPCControl
}

func (d dynamicTokenAuth) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	tok, err := d.control.getToken(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"authorization": "Bearer " + tok}, nil
}

func (d dynamicTokenAuth) RequireTransportSecurity() bool {
	return true
}

func (g *GRPCControl) dialConn(pool *x509.CertPool) (*grpc.ClientConn, error) {
	tlsCfg := &tls.Config{
		RootCAs:    pool,
		ServerName: ateAPIAudience,
		MinVersion: tls.VersionTLS12,
	}
	conn, err := grpc.NewClient(
		g.endpoint,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithPerRPCCredentials(dynamicTokenAuth{control: g}),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(32*1024*1024)),
	)
	if err != nil {
		return nil, fmt.Errorf("dialing ateapi %s: %w", g.endpoint, err)
	}
	return conn, nil
}

func (g *GRPCControl) getListClient(ctx context.Context) (ateapi.ControlClient, error) {
	g.mu.Lock()
	if g.listClient != nil {
		c := g.listClient
		g.mu.Unlock()
		return c, nil
	}
	g.mu.Unlock()

	pool, err := g.loadCertPool(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := g.dialConn(pool)
	if err != nil {
		return nil, err
	}

	g.mu.Lock()
	if g.listClient != nil {
		c := g.listClient
		g.mu.Unlock()
		_ = conn.Close()
		return c, nil
	}
	g.listConn = conn
	g.listClient = ateapi.NewControlClient(conn)
	c := g.listClient
	g.mu.Unlock()
	return c, nil
}

func (g *GRPCControl) getClient(ctx context.Context) (ateapi.ControlClient, error) {
	g.mu.Lock()
	if len(g.clients) > 0 {
		idx := int(g.rrSeq.Add(1) % uint64(len(g.clients)))
		c := g.clients[idx]
		g.mu.Unlock()
		return c, nil
	}
	g.mu.Unlock()

	pool, err := g.loadCertPool(ctx)
	if err != nil {
		return nil, err
	}

	conns := make([]*grpc.ClientConn, 0, controlConnPoolLen)
	clients := make([]ateapi.ControlClient, 0, controlConnPoolLen)
	for i := 0; i < controlConnPoolLen; i++ {
		conn, dialErr := g.dialConn(pool)
		if dialErr != nil {
			for _, c := range conns {
				_ = c.Close()
			}
			return nil, dialErr
		}
		conns = append(conns, conn)
		clients = append(clients, ateapi.NewControlClient(conn))
	}

	g.mu.Lock()
	if len(g.clients) > 0 {
		idx := int(g.rrSeq.Add(1) % uint64(len(g.clients)))
		c := g.clients[idx]
		g.mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		return c, nil
	}
	g.conns = conns
	g.clients = clients
	idx := int(g.rrSeq.Add(1) % uint64(len(g.clients)))
	c := g.clients[idx]
	g.mu.Unlock()
	return c, nil
}

// List returns every actor in the atespace by paging ListActors over gRPC.
func (g *GRPCControl) List(ctx context.Context) ([]Status, error) {
	client, err := g.getListClient(ctx)
	if err != nil {
		return nil, err
	}
	var out []Status
	var pageToken string
	for {
		resp, err := client.ListActors(ctx, &ateapi.ListActorsRequest{
			Atespace: g.atespace,
			Options: &ateapi.ListOptions{
				PageSize:  1000,
				PageToken: pageToken,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("ListActors: %w", err)
		}
		if out == nil {
			out = make([]Status, 0, len(resp.GetActors()))
		}
		for _, a := range resp.GetActors() {
			out = append(out, ActorProtoToStatus(a))
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}
	return out, nil
}

// Get fetches one actor's current status over gRPC via GetActor.
func (g *GRPCControl) Get(ctx context.Context, name string) (Status, error) {
	if !actorName.MatchString(name) {
		return Status{}, fmt.Errorf("invalid actor name %q", name)
	}
	client, err := g.getClient(ctx)
	if err != nil {
		return Status{}, err
	}
	a, err := client.GetActor(ctx, &ateapi.GetActorRequest{
		Actor: &ateapi.ObjectRef{
			Atespace: g.atespace,
			Name:     name,
		},
	})
	if err != nil {
		return Status{}, err
	}
	return ActorProtoToStatus(a), nil
}

// ListActiveSeats lists only actors currently assigned to worker pods in the
// target atespace by querying ListWorkers (~5 pages at 4,096 pods) and fanning
// out ListWorkerActorAssignments across occupied workers. This avoids paging
// 16,777,216 idle suspended actors on every poll tick.
func (g *GRPCControl) ListActiveSeats(ctx context.Context) ([]Status, error) {
	if _, err := g.getClient(ctx); err != nil {
		return nil, err
	}

	type occupiedWorker struct {
		name string
		pod  string
	}
	var occupied []occupiedWorker
	var pageToken string
	for {
		var (
			resp *ateapi.ListWorkersResponse
			err  error
		)
		for attempt := 0; attempt < 3; attempt++ {
			listClient, cErr := g.getClient(ctx)
			if cErr != nil {
				err = cErr
				break
			}
			callCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
			resp, err = listClient.ListWorkers(callCtx, &ateapi.ListWorkersRequest{
				Options: &ateapi.ListOptions{
					PageSize:  1000,
					PageToken: pageToken,
				},
			})
			cancel()
			if err == nil || ctx.Err() != nil {
				break
			}
		}
		if err != nil {
			return nil, fmt.Errorf("ListWorkers: %w", err)
		}
		for _, w := range resp.GetWorkers() {
			if w.GetWorkerNamespace() != g.atespace {
				continue
			}
			alloc := w.GetStatus().GetAllocated()
			if alloc.GetActors() <= 0 && alloc.GetCpuMillicores() <= 0 && alloc.GetMemoryBytes() <= 0 {
				continue
			}
			occupied = append(occupied, occupiedWorker{
				name: w.GetMetadata().GetName(),
				pod:  w.GetWorkerPod(),
			})
		}
		pageToken = resp.GetNextPageToken()
		if pageToken == "" {
			break
		}
	}

	if len(occupied) == 0 {
		return nil, nil
	}

	workerCh := make(chan occupiedWorker, len(occupied))
	for _, ow := range occupied {
		workerCh <- ow
	}
	close(workerCh)

	concurrency := min(len(occupied), 64)
	var (
		resMu            sync.Mutex
		out              []Status
		firstErr         error
		completedWorkers int
		wg               sync.WaitGroup
	)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ow := range workerCh {
				if ctx.Err() != nil {
					resMu.Lock()
					if firstErr == nil {
						firstErr = ctx.Err()
					}
					resMu.Unlock()
					return
				}
				var tok string
				var local []Status
				workerFailed := false
				for {
					var (
						aResp *ateapi.ListWorkerActorAssignmentsResponse
						aErr  error
					)
					for attempt := 0; attempt < 2; attempt++ {
						c, cErr := g.getClient(ctx)
						if cErr != nil {
							aErr = cErr
							break
						}
						timeout := 3 * time.Second
						if attempt > 0 {
							timeout = 2 * time.Second
						}
						callCtx, cancel := context.WithTimeout(ctx, timeout)
						aResp, aErr = c.ListWorkerActorAssignments(callCtx, &ateapi.ListWorkerActorAssignmentsRequest{
							Worker: &ateapi.ObjectRef{Name: ow.name},
							Options: &ateapi.ListOptions{
								PageSize:  1000,
								PageToken: tok,
							},
						})
						cancel()
						if aErr == nil || strings.Contains(aErr.Error(), "NotFound") || ctx.Err() != nil {
							break
						}
					}
					if aErr != nil {
						// A worker pod terminating mid-poll returns NotFound; skip it cleanly.
						if strings.Contains(aErr.Error(), "NotFound") {
							break
						}
						workerFailed = true
						resMu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("ListWorkerActorAssignments(%s): %w", ow.name, aErr)
						}
						resMu.Unlock()
						break
					}
					for _, asgn := range aResp.GetActorAssignments() {
						if asgn.GetActor().GetAtespace() != g.atespace {
							continue
						}
						actorID := asgn.GetActor().GetName()
						if actorID == "" {
							continue
						}
						local = append(local, Status{
							Name:      actorID,
							State:     types.StateRunning,
							WorkerPod: ow.pod,
						})
					}
					tok = aResp.GetNextPageToken()
					if tok == "" {
						break
					}
				}
				if !workerFailed {
					resMu.Lock()
					completedWorkers++
					if len(local) > 0 {
						out = append(out, local...)
					}
					resMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	missedWorkers := len(occupied) - completedWorkers
	maxToleratedFailures := max(8, len(occupied)/40)
	if firstErr != nil && (ctx.Err() != nil || len(out) == 0 || missedWorkers > maxToleratedFailures) {
		return nil, firstErr
	}
	return out, nil
}

type actorPageToken struct {
	V     int      `json:"v"`
	Kind  string   `json:"kind"`
	Scope string   `json:"scope"`
	Last  []string `json:"last"`
}

func encodeActorPageToken(atespace, lastName string) string {
	if lastName == "" {
		return ""
	}
	b, err := json.Marshal(actorPageToken{
		V:     1,
		Kind:  "actor",
		Scope: atespace,
		Last:  []string{lastName},
	})
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}

func isActorProtoCheckpointed(a *ateapi.Actor) bool {
	st := a.GetStatus()
	if st == nil {
		return false
	}
	state := st.GetState()
	if state == ateapi.ActorState_ACTOR_STATE_RUNNING ||
		state == ateapi.ActorState_ACTOR_STATE_RESUMING ||
		state == ateapi.ActorState_ACTOR_STATE_SUSPENDING {
		return true
	}
	for _, snap := range st.GetSnapshots() {
		if snap.GetOwner() == ateapi.SnapshotOwner_SNAPSHOT_OWNER_ACTOR {
			return true
		}
		if uri := snap.GetDurableSnapshot().GetObject().GetSnapshotUri(); uri != "" && !strings.Contains(uri, "/tags/") {
			return true
		}
	}
	return false
}

// HydrateCheckpointed scans the atespace's actors in parallel lexicographical
// key-range shards and invokes onBatch with 1-based actor indices that already
// have an actor-owned checkpoint or active state.
func (g *GRPCControl) HydrateCheckpointed(ctx context.Context, maxActors int, onBatch func(indices []int)) error {
	if maxActors <= 0 || onBatch == nil {
		return nil
	}
	if _, err := g.getClient(ctx); err != nil {
		return err
	}

	type lexShard struct {
		start string
		end   string
	}
	var shards []lexShard
	if maxActors > HyperFleetThreshold {
		bounds := make([]string, 0, 155)
		bounds = append(bounds, "")
		for p := 100; p <= 169; p++ {
			bounds = append(bounds, fmt.Sprintf("attendee-%d", p))
		}
		for p := 17; p <= 99; p++ {
			bounds = append(bounds, fmt.Sprintf("attendee-%d", p))
		}
		bounds = append(bounds, "")
		shards = make([]lexShard, 0, len(bounds)-1)
		for i := 0; i < len(bounds)-1; i++ {
			shards = append(shards, lexShard{start: bounds[i], end: bounds[i+1]})
		}
	} else {
		shards = []lexShard{{start: "", end: ""}}
	}

	shardCh := make(chan lexShard, len(shards))
	for _, sh := range shards {
		shardCh <- sh
	}
	close(shardCh)

	concurrency := min(len(shards), 6)
	var (
		errMu    sync.Mutex
		firstErr error
		wg       sync.WaitGroup
	)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			batch := make([]int, 0, 4096)
			for sh := range shardCh {
				if ctx.Err() != nil {
					return
				}
				pageToken := encodeActorPageToken(g.atespace, sh.start)
				for {
					c, cErr := g.getClient(ctx)
					if cErr != nil {
						errMu.Lock()
						if firstErr == nil {
							firstErr = cErr
						}
						errMu.Unlock()
						return
					}
					resp, lErr := c.ListActors(ctx, &ateapi.ListActorsRequest{
						Atespace: g.atespace,
						Options: &ateapi.ListOptions{
							PageSize:  1000,
							PageToken: pageToken,
						},
					})
					if lErr != nil {
						errMu.Lock()
						if firstErr == nil {
							firstErr = fmt.Errorf("HydrateCheckpointed ListActors(%q..%q): %w", sh.start, sh.end, lErr)
						}
						errMu.Unlock()
						return
					}
					reachedEnd := false
					for _, a := range resp.GetActors() {
						name := a.GetMetadata().GetName()
						if sh.end != "" && name > sh.end {
							reachedEnd = true
							break
						}
						if !isActorProtoCheckpointed(a) {
							continue
						}
						if idx, ok := ParseAttendeeIndex(name, maxActors); ok {
							batch = append(batch, idx)
							if len(batch) >= 4096 {
								onBatch(batch)
								batch = batch[:0]
							}
						}
					}
					if len(batch) > 0 {
						onBatch(batch)
						batch = batch[:0]
					}
					if reachedEnd || resp.GetNextPageToken() == "" {
						break
					}
					pageToken = resp.GetNextPageToken()
				}
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// Suspend asks ateapi to suspend one actor.
func (g *GRPCControl) Suspend(ctx context.Context, name string) error {
	if !actorName.MatchString(name) {
		return fmt.Errorf("invalid actor name %q", name)
	}
	client, err := g.getClient(ctx)
	if err != nil {
		return err
	}
	_, err = client.SuspendActor(ctx, &ateapi.SuspendActorRequest{
		Actor: &ateapi.ObjectRef{
			Atespace: g.atespace,
			Name:     name,
		},
	})
	return err
}

// Revert asks ateapi to roll one actor back to its last good snapshot.
func (g *GRPCControl) Revert(ctx context.Context, name string) error {
	if !actorName.MatchString(name) {
		return fmt.Errorf("invalid actor name %q", name)
	}
	client, err := g.getClient(ctx)
	if err != nil {
		return err
	}
	_, err = client.RevertActor(ctx, &ateapi.RevertActorRequest{
		Actor: &ateapi.ObjectRef{
			Atespace: g.atespace,
			Name:     name,
		},
	})
	return err
}

// DeleteActor deletes one actor over gRPC.
func (g *GRPCControl) DeleteActor(ctx context.Context, name string) error {
	if !actorName.MatchString(name) {
		return fmt.Errorf("invalid actor name %q", name)
	}
	client, err := g.getClient(ctx)
	if err != nil {
		return err
	}
	_, err = client.DeleteActor(ctx, &ateapi.DeleteActorRequest{
		Actor: &ateapi.ObjectRef{
			Atespace: g.atespace,
			Name:     name,
		},
	})
	return err
}

// CreateActor creates one actor from the given template over gRPC.
func (g *GRPCControl) CreateActor(ctx context.Context, name, template string) error {
	if !actorName.MatchString(name) {
		return fmt.Errorf("invalid actor name %q", name)
	}
	client, err := g.getClient(ctx)
	if err != nil {
		return err
	}
	_, err = client.CreateActor(ctx, &ateapi.CreateActorRequest{
		Actor: &ateapi.Actor{
			Metadata: &ateapi.ResourceMetadata{
				Atespace: g.atespace,
				Name:     name,
			},
			ActorTemplate: &ateapi.ObjectRef{
				Atespace: g.atespace,
				Name:     template,
			},
		},
	})
	return err
}

// ActorProtoToStatus converts an ateapi.Actor protobuf message into Status.
func ActorProtoToStatus(a *ateapi.Actor) Status {
	stateStr := strings.TrimPrefix(a.GetStatus().GetState().String(), "ACTOR_STATE_")
	state := types.SubstrateState(stateStr)
	if state == "" || state == "UNSPECIFIED" {
		state = types.StateUnknown
	}
	var (
		snapshotURI   string
		snapshotGen   int32 = -1
		inProgressURI string
		inProgressGen int32 = -1
	)
	for _, snap := range a.GetStatus().GetSnapshots() {
		ds := snap.GetDurableSnapshot()
		if ds == nil {
			continue
		}
		uri := ds.GetObject().GetSnapshotUri()
		switch ds.GetState() {
		case ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED:
			if snap.GetGeneration() >= snapshotGen {
				snapshotGen = snap.GetGeneration()
				snapshotURI = uri
			}
		case ateapi.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_PENDING:
			if snap.GetGeneration() >= inProgressGen {
				inProgressGen = snap.GetGeneration()
				inProgressURI = uri
			}
		}
	}
	var volumeType, volumeShare, volumeURI string
	if vols := a.GetStatus().GetExternalVolumes().GetVolumes(); len(vols) > 0 {
		vol := vols[0]
		for _, cand := range vols {
			if cand.GetName() == "workspace" {
				vol = cand
				break
			}
		}
		volumeType = vol.GetVolumeType()
		volumeURI = vol.GetStorageVolumeId()
		ctxMap := vol.GetVolumeContext()
		if ip, share := ctxMap["ip"], ctxMap["volume"]; ip != "" && share != "" {
			volumeShare = ip + ":/" + strings.TrimPrefix(share, "/")
		} else {
			volumeShare = vol.GetStorageVolumeId()
		}
	}
	return Status{
		Name:                  a.GetMetadata().GetName(),
		State:                 state,
		WorkerPod:             a.GetStatus().GetWorkerAssignment().GetWorkerPod(),
		InProgressSnapshotURI: inProgressURI,
		SnapshotURI:           snapshotURI,
		VolumeType:            volumeType,
		VolumeShare:           volumeShare,
		VolumeURI:             volumeURI,
	}
}
