package runner

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSemaphore_ImmediateAcquire(t *testing.T) {
	t.Parallel()
	s, _ := NewSemaphore(2)
	pos1, rel1 := s.Acquire()
	pos2, rel2 := s.Acquire()
	if pos1 != 0 || pos2 != 0 {
		t.Fatalf("expected immediate (0,0), got (%d,%d)", pos1, pos2)
	}
	rel1()
	rel2()
}

func TestSemaphore_QueuesAndPreservesOrder(t *testing.T) {
	t.Parallel()
	s, _ := NewSemaphore(1)

	_, rel0 := s.Acquire()

	var order []int32
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := int32(1); i <= 3; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			pos, rel := s.Acquire()
			mu.Lock()
			order = append(order, i)
			_ = pos
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			rel()
		}()
		time.Sleep(2 * time.Millisecond) // ensure deterministic queue order
	}

	// Initial holder releases — waiters drain in FIFO order.
	rel0()
	wg.Wait()

	if len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Fatalf("FIFO violated: %v", order)
	}
}

func TestSemaphore_QueuedCount(t *testing.T) {
	t.Parallel()
	s, _ := NewSemaphore(1)
	_, rel := s.Acquire()
	var queued int32
	var ready sync.WaitGroup
	ready.Add(1)
	go func() {
		atomic.AddInt32(&queued, 1)
		ready.Done()
		_, r := s.Acquire()
		r()
	}()
	ready.Wait()
	// Give the goroutine a moment to enter the wait queue.
	time.Sleep(5 * time.Millisecond)
	if got := s.QueuedCount(); got < 1 {
		t.Fatalf("expected QueuedCount >= 1, got %d", got)
	}
	rel()
}

func TestSemaphore_CapacityZeroIsError(t *testing.T) {
	t.Parallel()
	if _, err := NewSemaphore(0); err == nil {
		t.Fatal("expected error")
	}
}
