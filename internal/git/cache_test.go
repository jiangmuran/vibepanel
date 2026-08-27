package git

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The cache is tested through once() rather than through Read(), because what
// is worth pinning is the collapsing and not git's output — which the
// real-repository test above already covers.

func TestACachedAnswerIsNotReadTwice(t *testing.T) {
	c := &Cache{TTL: time.Minute}
	var runs atomic.Int32
	fn := func(context.Context) (any, error) {
		runs.Add(1)
		return "answer", nil
	}
	for i := 0; i < 5; i++ {
		v, err := c.once(context.Background(), "k", fn)
		if v != "answer" || err != nil {
			t.Fatalf("once = %v, %v", v, err)
		}
	}
	if runs.Load() != 1 {
		t.Errorf("ran %d times; five reads inside one TTL are one read", runs.Load())
	}
}

// The TTL is actually consulted, rather than every entry being a hit for ever.
//
// The two tests either side of this one both passed with the comparison
// replaced by `true` — one keeps its entry inside the window on purpose and the
// other switches caching off before the window is reached. An expiring entry is
// the only shape that says the number means anything.
func TestAnExpiredAnswerIsReadAgain(t *testing.T) {
	c := &Cache{TTL: 20 * time.Millisecond}
	var runs atomic.Int32
	fn := func(context.Context) (any, error) {
		runs.Add(1)
		return int(runs.Load()), nil
	}
	if v, _ := c.once(context.Background(), "k", fn); v != 1 {
		t.Fatalf("first read = %v", v)
	}
	if v, _ := c.once(context.Background(), "k", fn); v != 1 {
		t.Errorf("a read inside the TTL = %v, want the cached 1", v)
	}
	time.Sleep(40 * time.Millisecond)
	if v, _ := c.once(context.Background(), "k", fn); v != 2 {
		t.Errorf("a read after the TTL = %v; the entry never expired", v)
	}
}

func TestAStaleAnswerIsReadAgain(t *testing.T) {
	// A negative TTL is "no caching", which is what a test that counts
	// processes wants and what proves the TTL is consulted at all rather than
	// the map simply never being written.
	c := &Cache{TTL: -1}
	var runs atomic.Int32
	fn := func(context.Context) (any, error) {
		runs.Add(1)
		return runs.Load(), nil
	}
	for i := 0; i < 3; i++ {
		if _, err := c.once(context.Background(), "k", fn); err != nil {
			t.Fatal(err)
		}
	}
	if runs.Load() != 3 {
		t.Errorf("ran %d times; nothing should have been reused", runs.Load())
	}
}

// The half a TTL alone does not buy: everybody reloading at once is one read.
func TestSimultaneousReadersShareOneRun(t *testing.T) {
	c := &Cache{TTL: time.Minute}
	var runs atomic.Int32
	release := make(chan struct{})
	fn := func(context.Context) (any, error) {
		runs.Add(1)
		<-release
		return "answer", nil
	}

	const readers = 8
	var wg sync.WaitGroup
	got := make([]any, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, _ := c.once(context.Background(), "k", fn)
			got[i] = v
		}(i)
	}
	// Let them all arrive before the one in flight is allowed to finish.
	// Without single flight this is where the other seven fork their own.
	for waited := 0; waited < 200 && runs.Load() == 0; waited++ {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if runs.Load() != 1 {
		t.Errorf("ran %d times; eight simultaneous readers are one read", runs.Load())
	}
	for i, v := range got {
		if v != "answer" {
			t.Errorf("reader %d got %v", i, v)
		}
	}
}

// A viewer who navigated away must not take the read with them.
func TestOneCallerGivingUpDoesNotCancelTheSharedRead(t *testing.T) {
	c := &Cache{TTL: time.Minute}
	started := make(chan struct{})
	var sawCancel atomic.Bool
	fn := func(ctx context.Context) (any, error) {
		close(started)
		// If the read were tied to the first caller's request context, this is
		// where it would be cancelled out from under everybody waiting on it.
		select {
		case <-ctx.Done():
			sawCancel.Store(true)
		case <-time.After(50 * time.Millisecond):
		}
		return "answer", nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.once(ctx, "k", fn)
	}()
	<-started
	cancel()
	<-done

	if sawCancel.Load() {
		t.Error("the shared read was cancelled by the caller that started it")
	}
	v, err := c.once(context.Background(), "k", fn)
	if v != "answer" || err != nil {
		t.Errorf("once = %v, %v; the finished read should be the answer", v, err)
	}
}

// A waiter's own context still gets it out.
func TestAWaiterIsNotHeldBySomebodyElsesRead(t *testing.T) {
	c := &Cache{TTL: time.Minute}
	release := make(chan struct{})
	first := make(chan struct{})
	fn := func(context.Context) (any, error) {
		select {
		case first <- struct{}{}:
		default:
		}
		<-release
		return "answer", nil
	}
	go func() { _, _ = c.once(context.Background(), "k", fn) }()
	<-first

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.once(ctx, "k", fn); !errors.Is(err, context.Canceled) {
		t.Errorf("a waiter with a dead context got %v, want context.Canceled", err)
	}
	close(release)
}

// The two keys are separate, or a session's directory would be answered with
// the project root's log.
func TestReadAndStatusDoNotShareAnEntry(t *testing.T) {
	c := &Cache{TTL: time.Minute}
	var reads, statuses atomic.Int32
	if _, err := c.once(context.Background(), "read\x00/x", func(context.Context) (any, error) {
		reads.Add(1)
		return Snapshot{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.once(context.Background(), "status\x00/x", func(context.Context) (any, error) {
		statuses.Add(1)
		return Status{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 1 || statuses.Load() != 1 {
		t.Errorf("reads=%d statuses=%d; the same directory has two questions",
			reads.Load(), statuses.Load())
	}
}

// The default is below the tab's poll interval, or a lone viewer sees the same
// numbers twice and the tab looks stuck.
func TestTheDefaultTTLIsShorterThanTheTabPolls(t *testing.T) {
	// 5s is POLL_MS in web/src/components/panels/GitPanel.tsx. Nothing links
	// the two, so this is the note that says which number moves with which.
	if CacheTTL >= 5*time.Second {
		t.Errorf("CacheTTL = %v; the tab polls every 5s and would see stale numbers", CacheTTL)
	}
	c := &Cache{}
	if c.ttl() != CacheTTL {
		t.Errorf("the zero Cache uses %v, want CacheTTL", c.ttl())
	}
}

// Against a real repository, so the caching is proved on the thing it caches.
func TestTheCacheAnswersARealTree(t *testing.T) {
	dir := newRepo(t)
	c := &Cache{TTL: time.Minute}
	snap, err := c.Read(context.Background(), dir, 5)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !snap.Status.Repo || snap.Status.Branch != "main" {
		t.Errorf("%+v", snap.Status)
	}
	st, err := c.Status(context.Background(), dir)
	if err != nil || !st.Repo {
		t.Errorf("Status = %+v, %v", st, err)
	}
	if _, err := c.Read(context.Background(), t.TempDir(), 5); !errors.Is(err, ErrNotARepo) {
		t.Errorf("a plain directory through the cache = %v, want ErrNotARepo", err)
	}
}

// An entry nothing has asked for in a long time does not live for ever.
func TestIdleEntriesAreSwept(t *testing.T) {
	c := &Cache{TTL: time.Millisecond}
	fn := func(context.Context) (any, error) { return 1, nil }
	if _, err := c.once(context.Background(), "old", fn); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.entries["old"].at = time.Now().Add(-2 * cacheIdle)
	c.mu.Unlock()
	// The sweep runs on a miss, which is the only moment the map grows.
	if _, err := c.once(context.Background(), "new", fn); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	_, still := c.entries["old"]
	c.mu.Unlock()
	if still {
		t.Error("an entry idle for twice cacheIdle is still held")
	}
}
