package geoparser

import (
	"sync"

	"github.com/royalcat/btrgo/btrchannels"
)

type Stager[D any] struct {
	mu     sync.Mutex
	buffer *btrchannels.InfiniteChannel[D]

	stageCount int
	filter     func(D) int
	queue      []chan D
	handlers   []func(data D) error
	workersWG  []sync.WaitGroup

	workers int
}

func NewStager[D any](filter func(D) int, handlers []func(data D) error) *Stager[D] {

	return &Stager[D]{
		buffer:    btrchannels.NewInfiniteChannel[D](),
		filter:    filter,
		handlers:  handlers,
		workersWG: make([]sync.WaitGroup, len(handlers)),
	}
}

func (s *Stager[D]) NewStage() *Stage[D] {
	s.mu.Lock()
	defer s.mu.Unlock()

	stage := &Stage[D]{
		i: s.stageCount,
	}
	s.stageCount++

	return stage
}

func (s *Stager[D]) Submit(data D) {
	s.buffer.In() <- data
}

func (s *Stager[D]) WaitAndClose() {
	for i := 0; i < s.stageCount; i++ {
		close(s.queue[i])
		s.workersWG[i].Wait()
	}
}

func (s *Stager[D]) setStageHandler(i int, h func(data D) error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.handlers[i] = h
}

func (s *Stager[D]) submitStage(i int, data D) {
	s.queue[i] <- data
}

func (s *Stager[D]) waitAndCloseStage(i int) {
	close(s.queue[i])
	s.workersWG[i].Wait()
}

type Stage[D any] struct {
	stager *Stager[D]
	i      int
}

func (s *Stage[D]) SetHandler(h func(data D) error) {
	s.stager.setStageHandler(s.i, h)
}

func (s *Stage[D]) Submit(data D) {
	s.stager.submitStage(s.i, data)
}

func (s *Stage[D]) WaitAndClose() {
	s.stager.waitAndCloseStage(s.i)
}

func (s *Stager[D]) Run() {

}
