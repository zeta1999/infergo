package ad

// Multi-threaded tape store, suitable for running
// multiple goroutines with inference in parallel.

import (
	"sync"
)

type mtStore struct {
	sync.Mutex
	store map[int64]*adTape
}

func newStore() *mtStore {
	return &mtStore{
		store: map[int64]*adTape{},
	}
}

// MTSafeOn makes differentiation thread safe at
// the expense of a loss in performance. There is
// no corresponding MTSafeOff, as once things are
// safe they cannot safely become unsafe again.
func MTSafeOn() {
	tapes = newStore()
	mtSafe = true
}

func (tapes *mtStore) get() *adTape {
	id := goid()
	tapes.Lock()
	tape, ok := tapes.store[id]
	tapes.Unlock()
	if !ok {
		tape = newTape()
		tapes.Lock()
		tapes.store[id] = tape
		tapes.Unlock()
	}
	return tape
}

func(tapes *mtStore) drop() {
	id := goid()
	tapes.Lock()
	delete(tapes.store, id)
	tapes.Unlock()
}

func(_ *mtStore) clear() {
	tapes = newStore()
}
