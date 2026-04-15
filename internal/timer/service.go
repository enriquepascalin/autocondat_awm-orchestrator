package timer

import (
	"context"
	"log"
	"time"

	"github.com/enriquepascalin/awm-orchestrator/internal/runtime"
	"github.com/enriquepascalin/awm-orchestrator/internal/store"
)

type Service struct {
	store  store.Store
	engine *runtime.Engine
	stopCh chan struct{}
}

func NewService(s store.Store, e *runtime.Engine) *Service {
	return &Service{store: s, engine: e, stopCh: make(chan struct{})}
}

func (s *Service) Start(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.checkTimers(ctx)
		}
	}
}

func (s *Service) checkTimers(ctx context.Context) {
	timers, err := s.store.GetPendingTimers(ctx, time.Now())
	if err != nil {
		log.Printf("Timer check error: %v", err)
		return
	}
	for _, t := range timers {
		if err := s.store.MarkTimerFired(ctx, t.ID); err != nil {
			continue
		}
		if err := s.engine.ResumeAfterTimer(ctx, &t); err != nil {
			log.Printf("Failed to resume workflow after timer: %v", err)
		}
	}
}

func (s *Service) Stop() {
	close(s.stopCh)
}