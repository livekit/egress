// Copyright 2026 LiveKit, Inc.
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

package info

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/linkdata/deadlock"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/psrpc"

	"github.com/livekit/egress/pkg/config"
)

// errServerDeadlineExceeded is what the client sees when the IOInfo server gives up on its own deadline,
// e.g. while waiting for a database connection.
var errServerDeadlineExceeded = psrpc.NewErrorf(psrpc.DeadlineExceeded, "deadline exceeded")

type fakeIOInfo struct {
	rpc.IOInfoClient

	// before runs ahead of each attempt, outside the lock, so a test can hold an attempt in flight
	before func(info *livekit.EgressInfo)

	// createBefore runs ahead of CreateEgress, outside the lock, so a test can hold a create in flight
	createBefore func()

	mu        deadlock.Mutex
	createErr error
	fail      func(info *livekit.EgressInfo, attempt int) error
	attempts  map[string]int
	received  []*livekit.EgressInfo
}

func newFakeIOInfo(fail func(info *livekit.EgressInfo, attempt int) error) *fakeIOInfo {
	return &fakeIOInfo{
		fail:     fail,
		attempts: make(map[string]int),
	}
}

func (f *fakeIOInfo) CreateEgress(_ context.Context, _ *livekit.EgressInfo, _ ...psrpc.RequestOption) (*emptypb.Empty, error) {
	if f.createBefore != nil {
		f.createBefore()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &emptypb.Empty{}, nil
}

func (f *fakeIOInfo) setCreateErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createErr = err
}

func (f *fakeIOInfo) UpdateEgress(_ context.Context, info *livekit.EgressInfo, _ ...psrpc.RequestOption) (*emptypb.Empty, error) {
	if f.before != nil {
		f.before(info)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.attempts[info.EgressId]++
	if err := f.fail(info, f.attempts[info.EgressId]); err != nil {
		return nil, err
	}
	f.received = append(f.received, info)
	return &emptypb.Empty{}, nil
}

func (f *fakeIOInfo) attemptsFor(egressID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts[egressID]
}

func (f *fakeIOInfo) receivedFor(egressID string) []livekit.EgressStatus {
	f.mu.Lock()
	defer f.mu.Unlock()

	var statuses []livekit.EgressStatus
	for _, info := range f.received {
		if info.EgressId == egressID {
			statuses = append(statuses, info.Status)
		}
	}
	return statuses
}

func counterValue(c prometheus.Counter) float64 {
	m := &dto.Metric{}
	_ = c.Write(m)
	return m.GetCounter().GetValue()
}

func newTestReporter(client rpc.IOInfoClient, workers int) *sessionReporter {
	conf := &config.BaseConfig{
		IOUpdateTimeout:       time.Second,
		IOUpdateRetryDeadline: time.Minute,
		IOWorkers:             workers,
	}
	return newSessionReporter(conf, client, newIOUpdateFailures(conf))
}

func egressInfo(egressID string, status livekit.EgressStatus) *livekit.EgressInfo {
	return &livekit.EgressInfo{
		EgressId:  egressID,
		Status:    status,
		UpdatedAt: time.Now().UnixNano(),
	}
}

func TestUpdateEgressRetriesServerDeadlineExceeded(t *testing.T) {
	io := newFakeIOInfo(func(_ *livekit.EgressInfo, attempt int) error {
		if attempt <= 2 {
			return errServerDeadlineExceeded
		}
		return nil
	})
	c := newTestReporter(io, 1)

	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_COMPLETE)))

	require.Eventually(t, func() bool {
		return len(io.receivedFor("EG_A")) == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, []livekit.EgressStatus{livekit.EgressStatus_EGRESS_COMPLETE}, io.receivedFor("EG_A"))
	require.Equal(t, 3, io.attemptsFor("EG_A"))
}

func TestNonRetryableErrorDropsUpdate(t *testing.T) {
	io := newFakeIOInfo(func(_ *livekit.EgressInfo, _ int) error {
		return psrpc.NewErrorf(psrpc.Internal, "internal error")
	})
	c := newTestReporter(io, 1)

	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_COMPLETE)))

	require.Eventually(t, func() bool {
		return io.attemptsFor("EG_A") == 1
	}, time.Second, 10*time.Millisecond)
	time.Sleep(2 * initialBackoff)
	require.Equal(t, 1, io.attemptsFor("EG_A"))
}

func TestAbandonedUpdateDoesNotBlockNextUpdate(t *testing.T) {
	io := newFakeIOInfo(func(info *livekit.EgressInfo, _ int) error {
		if info.Status == livekit.EgressStatus_EGRESS_ACTIVE {
			return errServerDeadlineExceeded
		}
		return nil
	})
	conf := &config.BaseConfig{
		IOUpdateTimeout:       time.Second,
		IOUpdateRetryDeadline: 600 * time.Millisecond,
		IOWorkers:             1,
	}
	c := newSessionReporter(conf, io, newIOUpdateFailures(conf))

	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_ACTIVE)))
	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_COMPLETE)))

	// ACTIVE is given up at its deadline, and COMPLETE, queued behind it, is still sent
	require.Eventually(t, func() bool {
		return len(io.receivedFor("EG_A")) == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, []livekit.EgressStatus{livekit.EgressStatus_EGRESS_COMPLETE}, io.receivedFor("EG_A"))
}

func TestFailingUpdateDoesNotBlockOtherEgresses(t *testing.T) {
	io := newFakeIOInfo(func(info *livekit.EgressInfo, _ int) error {
		if info.EgressId == "EG_A" {
			return errServerDeadlineExceeded
		}
		return nil
	})
	// a single worker puts both egresses on the same queue
	c := newTestReporter(io, 1)

	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_COMPLETE)))
	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_B", livekit.EgressStatus_EGRESS_COMPLETE)))

	// EG_B is delivered while EG_A is still retrying
	require.Eventually(t, func() bool {
		return len(io.receivedFor("EG_B")) == 1
	}, time.Second, 10*time.Millisecond)

	// EG_A keeps retrying in the background
	require.Eventually(t, func() bool {
		return io.attemptsFor("EG_A") >= 2
	}, 5*time.Second, 10*time.Millisecond)
	require.Empty(t, io.receivedFor("EG_A"))
}

func TestUpdatesQueuedBehindRetryAreDeliveredInOrder(t *testing.T) {
	io := newFakeIOInfo(func(_ *livekit.EgressInfo, attempt int) error {
		if attempt == 1 {
			return errServerDeadlineExceeded
		}
		return nil
	})
	c := newTestReporter(io, 1)

	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_ACTIVE)))
	require.Eventually(t, func() bool {
		return io.attemptsFor("EG_A") == 1
	}, time.Second, 10*time.Millisecond)

	// arrives while the ACTIVE update waits out its backoff
	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_COMPLETE)))

	require.Eventually(t, func() bool {
		return len(io.receivedFor("EG_A")) == 2
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, []livekit.EgressStatus{
		livekit.EgressStatus_EGRESS_ACTIVE,
		livekit.EgressStatus_EGRESS_COMPLETE,
	}, io.receivedFor("EG_A"))
}

func TestUpdatesQueuedWhileInFlightAreAllDelivered(t *testing.T) {
	inFlight := make(chan struct{})
	release := make(chan struct{})
	io := newFakeIOInfo(func(_ *livekit.EgressInfo, _ int) error {
		return nil
	})
	io.before = func(info *livekit.EgressInfo) {
		if info.Status == livekit.EgressStatus_EGRESS_ACTIVE {
			close(inFlight)
			<-release
		}
	}
	c := newTestReporter(io, 1)

	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_ACTIVE)))
	<-inFlight

	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_ENDING)))
	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_COMPLETE)))
	close(release)

	require.Eventually(t, func() bool {
		return len(io.receivedFor("EG_A")) == 3
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, []livekit.EgressStatus{
		livekit.EgressStatus_EGRESS_ACTIVE,
		livekit.EgressStatus_EGRESS_ENDING,
		livekit.EgressStatus_EGRESS_COMPLETE,
	}, io.receivedFor("EG_A"))
}

func TestDrainWaitsForPendingRetry(t *testing.T) {
	io := newFakeIOInfo(func(_ *livekit.EgressInfo, attempt int) error {
		if attempt == 1 {
			return errServerDeadlineExceeded
		}
		return nil
	})
	c := newTestReporter(io, 1)

	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_COMPLETE)))
	require.Eventually(t, func() bool {
		return io.attemptsFor("EG_A") == 1
	}, time.Second, 10*time.Millisecond)

	c.Drain()
	require.Equal(t, []livekit.EgressStatus{livekit.EgressStatus_EGRESS_COMPLETE}, io.receivedFor("EG_A"))
}

func TestFailedCreateDiscardsLaterUpdates(t *testing.T) {
	io := newFakeIOInfo(func(_ *livekit.EgressInfo, _ int) error {
		return nil
	})
	io.setCreateErr(errServerDeadlineExceeded)
	c := newTestReporter(io, 1)

	require.Error(t, <-c.CreateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_STARTING)))

	// the aborted handler reports FAILED, which is never sent
	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_FAILED)))
	time.Sleep(2 * initialBackoff)
	require.Zero(t, io.attemptsFor("EG_A"))
}

func TestLaunchFailureAfterCreateIsRetried(t *testing.T) {
	io := newFakeIOInfo(func(_ *livekit.EgressInfo, attempt int) error {
		if attempt == 1 {
			return errServerDeadlineExceeded
		}
		return nil
	})
	c := newTestReporter(io, 1)

	require.NoError(t, <-c.CreateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_STARTING)))
	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_FAILED)))

	require.Eventually(t, func() bool {
		return len(io.receivedFor("EG_A")) == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, []livekit.EgressStatus{livekit.EgressStatus_EGRESS_FAILED}, io.receivedFor("EG_A"))
}

func TestUpdateAfterSessionEndedForFailedCreateIsDiscarded(t *testing.T) {
	io := newFakeIOInfo(func(_ *livekit.EgressInfo, _ int) error {
		return nil
	})
	io.setCreateErr(errServerDeadlineExceeded)
	c := newTestReporter(io, 1)

	require.Error(t, <-c.CreateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_STARTING)))

	// a HandlerFinished in flight when the handler died abnormally reports after SessionEnded
	c.SessionEnded(context.Background(), "EG_A")
	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_FAILED)))
	time.Sleep(2 * initialBackoff)
	require.Zero(t, io.attemptsFor("EG_A"))
}

func TestNewCreateClearsFailedCreateMark(t *testing.T) {
	io := newFakeIOInfo(func(_ *livekit.EgressInfo, _ int) error {
		return nil
	})
	io.setCreateErr(errServerDeadlineExceeded)
	c := newTestReporter(io, 1)

	require.Error(t, <-c.CreateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_STARTING)))

	io.setCreateErr(nil)
	require.NoError(t, <-c.CreateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_STARTING)))
	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_COMPLETE)))

	require.Eventually(t, func() bool {
		return len(io.receivedFor("EG_A")) == 1
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, []livekit.EgressStatus{livekit.EgressStatus_EGRESS_COMPLETE}, io.receivedFor("EG_A"))
}

func TestCreateReportedAsFailedOnFullQueueDiscardsLaterUpdates(t *testing.T) {
	blockInFlight := make(chan struct{})
	releaseBlock := make(chan struct{})
	t.Cleanup(func() { close(releaseBlock) })
	createInFlight := make(chan struct{})
	releaseCreate := make(chan struct{})

	io := newFakeIOInfo(func(_ *livekit.EgressInfo, _ int) error {
		return nil
	})
	io.before = func(info *livekit.EgressInfo) {
		if info.EgressId == "EG_BLOCK" {
			close(blockInFlight)
			<-releaseBlock
		}
	}
	io.createBefore = func() {
		close(createInFlight)
		<-releaseCreate
	}
	c := newTestReporter(io, 1)
	w := c.workers[0]

	// hold the worker and fill its queue
	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_BLOCK", livekit.EgressStatus_EGRESS_ACTIVE)))
	<-blockInFlight
	for i := 0; i < cap(w.queue); i++ {
		w.queue <- fmt.Sprintf("EG_FILL_%d", i)
	}

	// the create succeeds, but its buffered update cannot be queued, so the start is reported as failed
	errChan := c.CreateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_STARTING))
	<-createInFlight
	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_ACTIVE)))
	close(releaseCreate)
	require.Error(t, <-errChan)
	require.Equal(t, 1.0, counterValue(c.ioUpdateFailures.WithLabelValues(ioUpdateAbandoned)))

	// the aborted handler's FAILED is discarded
	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_A", livekit.EgressStatus_EGRESS_FAILED)))
	w.mu.Lock()
	_, pending := w.pending["EG_A"]
	w.mu.Unlock()
	require.False(t, pending)
	require.Zero(t, io.attemptsFor("EG_A"))
	require.Equal(t, 1.0, counterValue(c.ioUpdateFailures.WithLabelValues(ioUpdateUnowned)))
}

func TestCreateSweepsExpiredFailedCreateMarks(t *testing.T) {
	io := newFakeIOInfo(func(_ *livekit.EgressInfo, _ int) error {
		return nil
	})
	c := newTestReporter(io, 1)
	w := c.workers[0]

	w.mu.Lock()
	w.createFailed["EG_OLD"] = time.Now().Add(-2 * createFailedTTL)
	w.mu.Unlock()

	// any create sweeps marks past createFailedTTL, so updates for EG_OLD are sent again
	require.NoError(t, <-c.CreateEgress(context.Background(), egressInfo("EG_NEW", livekit.EgressStatus_EGRESS_STARTING)))
	require.NoError(t, c.UpdateEgress(context.Background(), egressInfo("EG_OLD", livekit.EgressStatus_EGRESS_COMPLETE)))

	require.Eventually(t, func() bool {
		return len(io.receivedFor("EG_OLD")) == 1
	}, 5*time.Second, 10*time.Millisecond)
}

func TestUpdatesBeyondCapCoalesceWithinStatus(t *testing.T) {
	io := newFakeIOInfo(func(_ *livekit.EgressInfo, attempt int) error {
		if attempt == 1 {
			return errServerDeadlineExceeded
		}
		return nil
	})
	c := newTestReporter(io, 1)

	withDetails := func(status livekit.EgressStatus, details string) *livekit.EgressInfo {
		info := egressInfo("EG_A", status)
		info.Details = details
		return info
	}

	require.NoError(t, c.UpdateEgress(context.Background(), withDetails(livekit.EgressStatus_EGRESS_ACTIVE, "0")))
	require.Eventually(t, func() bool {
		return io.attemptsFor("EG_A") == 1
	}, time.Second, 10*time.Millisecond)

	// queued while the first update waits out its backoff
	for i := 1; i < maxPendingUpdates+50; i++ {
		require.NoError(t, c.UpdateEgress(context.Background(), withDetails(livekit.EgressStatus_EGRESS_ACTIVE, fmt.Sprint(i))))
	}
	require.NoError(t, c.UpdateEgress(context.Background(), withDetails(livekit.EgressStatus_EGRESS_COMPLETE, "final")))

	// updates past the cap replace the last ACTIVE, and the status change is still delivered
	require.Eventually(t, func() bool {
		return len(io.receivedFor("EG_A")) == maxPendingUpdates+1
	}, 5*time.Second, 10*time.Millisecond)

	io.mu.Lock()
	defer io.mu.Unlock()
	n := len(io.received)
	require.Equal(t, fmt.Sprint(maxPendingUpdates+49), io.received[n-2].Details)
	require.Equal(t, livekit.EgressStatus_EGRESS_COMPLETE, io.received[n-1].Status)
}
