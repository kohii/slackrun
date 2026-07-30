package slackapp

import "sync"

// restartGate makes dispatch admission and idle restart preparation one
// atomic decision. Once prepared, the process stays quiesced until replaced.
type restartGate struct {
	mu       sync.Mutex
	prepared bool
	active   int
}

func (g *restartGate) admit() (release func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.prepared {
		return nil, false
	}
	g.active++
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.active--
			g.mu.Unlock()
		})
	}, true
}

func (g *restartGate) prepare() (active int, ready bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.prepared {
		return 0, true
	}
	if g.active > 0 {
		return g.active, false
	}
	g.prepared = true
	return 0, true
}
