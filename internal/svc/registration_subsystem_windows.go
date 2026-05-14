//go:build windows

package svc

import (
	"context"
	"fmt"
	"sync"

	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/dashboard"
	"github.com/LISSConsulting/LISSTech.DrainCtl/internal/lifecycle"
)

var _ lifecycle.Subsystem = (*registrationSubsystem)(nil)

type dashboardRegistrar func(context.Context, string) (*dashboard.RegisterResult, error)

type registrationRequest struct {
	ctx   context.Context
	url   string
	reply chan registrationResponse
}

type registrationResponse struct {
	result *dashboard.RegisterResult
	err    error
}

// registrationSubsystem owns dashboard registration work requested through the
// service named pipe. This keeps the pipe handler from launching unmanaged
// network calls and gives service shutdown a single Stop point to cancel them.
type registrationSubsystem struct {
	register dashboardRegistrar
	requests chan registrationRequest

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	started bool
	stopped bool

	wg       sync.WaitGroup
	stopOnce sync.Once
}

func newRegistrationSubsystem(register dashboardRegistrar) *registrationSubsystem {
	return &registrationSubsystem{
		register: register,
		requests: make(chan registrationRequest),
	}
}

func (s *registrationSubsystem) Start(ctx context.Context) error {
	derived, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.ctx = derived
	s.cancel = cancel
	s.started = true
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(derived)
	}()
	return nil
}

func (s *registrationSubsystem) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-s.requests:
			s.handle(ctx, req)
		}
	}
}

func (s *registrationSubsystem) handle(parent context.Context, req registrationRequest) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(req.ctx, cancel)
	result, err := s.register(ctx, req.url)
	if !stop() && req.ctx.Err() != nil && err == nil {
		err = req.ctx.Err()
	}
	cancel()

	select {
	case req.reply <- registrationResponse{result: result, err: err}:
	case <-parent.Done():
	case <-req.ctx.Done():
	}
}

func (s *registrationSubsystem) Register(ctx context.Context, dashboardURL string) (*dashboard.RegisterResult, error) {
	s.mu.Lock()
	serviceCtx := s.ctx
	running := s.started && !s.stopped && serviceCtx != nil
	s.mu.Unlock()
	if !running {
		return nil, fmt.Errorf("registration subsystem not running")
	}

	req := registrationRequest{
		ctx:   ctx,
		url:   dashboardURL,
		reply: make(chan registrationResponse, 1),
	}
	select {
	case s.requests <- req:
	case <-serviceCtx.Done():
		return nil, serviceCtx.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case resp := <-req.reply:
		return resp.result, resp.err
	case <-serviceCtx.Done():
		return nil, serviceCtx.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *registrationSubsystem) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopped = true
		cancel := s.cancel
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		s.wg.Wait()
	})
}
