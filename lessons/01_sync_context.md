# Module 01: mutexes, sync.Once, context cancellation

## Primer: sync & context

**A DATA RACE:** two goroutines touch the same memory at the same time and at least one writes.
Maps are not safe for concurrent use: Go can crash with "fatal error: concurrent map writes".
Find races with the race detector:   go test -race ./...

sync.Mutex: only one goroutine at a time inside the locked section

```
c.mu.Lock()
defer c.mu.Unlock()        // defer guarantees the unlock on every return path
(sync.RWMutex: many readers OR one writer: RLock/RUnlock for reads)
Never copy a struct that contains a Mutex. Use a pointer.
```

sync.Once: run something exactly once, even with many callers   once.Do(func() { ... })
sync/atomic: lock-free counters   var n atomic.Int64;  n.Add(1);  n.Load()

context.Context: carries cancellation and deadlines down a call tree. By convention it's the FIRST
parameter, named ctx.

```
ctx, cancel := context.WithTimeout(parent, 2*time.Second)
defer cancel()                     // always call cancel to release resources
select {
case <-ctx.Done():                 // closed when cancelled or when the deadline passes
    return ctx.Err()               // context.Canceled or context.DeadlineExceeded
case v := <-work:
    ...
}
context.WithCancel(parent) returns a cancel() func you call yourself.
Cancelling a context cancels every context derived from it.
```

**FROM JS:** ctx is the AbortController/AbortSignal of Go, but it's passed explicitly through every call.

## Lesson 1.1: SafeCounter

**CONCEPT:** protecting shared state with a mutex

```
func (s *Store) Set(k string, v int) {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.data[k] = v
}
Lock in EVERY method that touches the shared data, reads included. Run with -race to prove it.
```

**TASK:** Inc adds 1 to the count for key; Get returns the current count (0 for unknown keys).
Many goroutines will call them at once.

**Worked example:**

```go
type SafeCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func NewSafeCounter() *SafeCounter {
	return &SafeCounter{counts: make(map[string]int)}
}

func (c *SafeCounter) Inc(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[key]++
}

func (c *SafeCounter) Get(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[key]
}
```

## Lesson 1.2: FetchAll

**CONCEPT:** context cancellation across several goroutines

```
child, cancel := context.WithCancel(parent)   // derive a context you can cancel yourself
defer cancel()                                // ...and always clean it up
Goroutines can each write their OWN slot of a pre-sized slice without a lock:  results[i] = ...
A non-blocking channel check:  select { case v := <-ch: ...; default: /* nothing waiting */ }
Think about: where do goroutines report errors, and what should happen to the siblings when one fails?
```

**TASK:** Call fetch(ctx, id) for every id concurrently. Return the results in the same order as ids.
If any call fails, cancel the others and return that first error. If the caller's ctx is cancelled or
times out, the fetches see it through the ctx you pass them, so just return the error they report.
(The tests use fake fetch functions, so nothing touches the network.)

**Worked example:**

```go
// Fetcher loads one item. It should give up early if ctx is cancelled.
type Fetcher func(ctx context.Context, id int) (string, error)

func FetchAll(ctx context.Context, ids []int, fetch Fetcher) ([]string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]string, len(ids))
	errs := make(chan error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := fetch(ctx, id)
			if err != nil {
				errs <- err
				cancel()
				return
			}
			results[i] = s
		}()
	}
	wg.Wait()

	select {
	case err := <-errs:
		return nil, err
	default:
		return results, nil
	}
}
```

## Lesson 1.3: LazyValue (bonus)

**CONCEPT:** sync.Once

```
var once sync.Once
once.Do(func() { expensiveSetup() })   // runs once in total; other callers wait for it to finish
```

**TASK:** Get returns the value, calling load() only on the FIRST call, even if many goroutines call Get at once.

**Worked example:**

```go
type LazyValue struct {
	once sync.Once
	val  string
}

func (l *LazyValue) Get(load func() string) string {
	l.once.Do(func() { l.val = load() })
	return l.val
}
```
