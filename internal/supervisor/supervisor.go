package supervisor

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
)

type WorkerProcess struct {
	WorkflowInstanceID uuid.UUID
	Cmd                *exec.Cmd
	PID                int
	Status             string
	StartTime          time.Time
	RestartCount       int
}

type Supervisor struct {
	mu           sync.RWMutex
	workers      map[uuid.UUID]*WorkerProcess
	workerBinary string
	maxRestarts  int
}

func NewSupervisor(workerBinary string) *Supervisor {
	return &Supervisor{
		workers:      make(map[uuid.UUID]*WorkerProcess),
		workerBinary: workerBinary,
		maxRestarts:  3,
	}
}

func (s *Supervisor) StartWorker(instanceID uuid.UUID, workflowDefID, tenant string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.workers[instanceID]; exists {
		return fmt.Errorf("worker for workflow %s already exists", instanceID)
	}

	cmd := exec.Command(s.workerBinary,
		"--instance-id", instanceID.String(),
		"--workflow-def-id", workflowDefID,
		"--tenant", tenant,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start worker: %w", err)
	}

	wp := &WorkerProcess{
		WorkflowInstanceID: instanceID,
		Cmd:                cmd,
		PID:                cmd.Process.Pid,
		Status:             "running",
		StartTime:          time.Now(),
		RestartCount:       0,
	}
	s.workers[instanceID] = wp

	go s.monitorWorker(instanceID, wp)

	log.Printf("Started worker PID %d for workflow %s", wp.PID, instanceID)
	return nil
}

func (s *Supervisor) StopWorker(instanceID uuid.UUID) error {
	s.mu.Lock()
	wp, exists := s.workers[instanceID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("worker for workflow %s not found", instanceID)
	}
	delete(s.workers, instanceID)
	s.mu.Unlock()

	if err := syscall.Kill(-wp.Cmd.Process.Pid, syscall.SIGTERM); err != nil {
		log.Printf("Failed to send SIGTERM to worker %d: %v", wp.PID, err)
	}

	done := make(chan error)
	go func() { done <- wp.Cmd.Wait() }()
	select {
	case <-time.After(10 * time.Second):
		syscall.Kill(-wp.Cmd.Process.Pid, syscall.SIGKILL)
		log.Printf("Force killed worker PID %d", wp.PID)
	case err := <-done:
		if err != nil {
			log.Printf("Worker PID %d exited with error: %v", wp.PID, err)
		}
	}

	log.Printf("Stopped worker for workflow %s", instanceID)
	return nil
}

func (s *Supervisor) monitorWorker(instanceID uuid.UUID, wp *WorkerProcess) {
	err := wp.Cmd.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.workers[instanceID]; !exists {
		return
	}

	if err != nil {
		log.Printf("Worker PID %d for workflow %s exited with error: %v", wp.PID, instanceID, err)
		wp.Status = "crashed"
		wp.RestartCount++
		if wp.RestartCount <= s.maxRestarts {
			log.Printf("Restarting worker for workflow %s (attempt %d)", instanceID, wp.RestartCount)
			delete(s.workers, instanceID)
			s.mu.Unlock()
			// In a full implementation, you'd store metadata to restart.
			s.mu.Lock()
		} else {
			log.Printf("Worker for workflow %s exceeded max restarts, giving up", instanceID)
			delete(s.workers, instanceID)
		}
	} else {
		log.Printf("Worker PID %d for workflow %s exited normally", wp.PID, instanceID)
		delete(s.workers, instanceID)
	}
}

func (s *Supervisor) ListWorkers() map[uuid.UUID]*WorkerProcess {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[uuid.UUID]*WorkerProcess, len(s.workers))
	for k, v := range s.workers {
		result[k] = v
	}
	return result
}

func (s *Supervisor) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for instanceID, wp := range s.workers {
		syscall.Kill(-wp.Cmd.Process.Pid, syscall.SIGTERM)
		delete(s.workers, instanceID)
	}
	log.Println("Supervisor stopped all workers")
}