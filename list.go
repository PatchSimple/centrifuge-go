package centrifuge

import (
	"container/list"
	"context"
	"sync"
)

type List[T any] struct {
	mu   sync.Mutex
	list *list.List
	cond *sync.Cond
}

func NewList[T any]() *List[T] {
	l := &List[T]{
		list: list.New(),
	}
	l.cond = sync.NewCond(&l.mu)
	return l
}

func (l *List[T]) PushBack(value T) {
	l.mu.Lock()
	l.list.PushBack(value)
	l.cond.Signal()
	l.mu.Unlock()
}

func (l *List[T]) PopFront() (T, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.list.Len() == 0 {
		var zero T
		return zero, false
	}
	e := l.list.Front()
	val := e.Value.(T)
	l.list.Remove(e)
	return val, true
}

func (l *List[T]) PopFrontCtx(ctx context.Context) (T, error) {
	var zero T
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	default:
	}
	itemCh := make(chan T)
	errCh := make(chan error)
	go func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		for l.list.Len() == 0 {
			l.cond.Wait()
			if ctx.Err() != nil {
				errCh <- ctx.Err()
				return
			}
		}
		e := l.list.Front()
		val := e.Value.(T)
		l.list.Remove(e)
		itemCh <- val
	}()
	select {
	case v := <-itemCh:
		return v, nil
	case err := <-errCh:
		return zero, err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (l *List[T]) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.list.Len()
}
