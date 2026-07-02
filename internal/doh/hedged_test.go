package doh_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/vitus9988/psdns/internal/doh"
)

type exchangerFunc func(context.Context, *dns.Msg) (*dns.Msg, error)

func (f exchangerFunc) Exchange(ctx context.Context, q *dns.Msg) (*dns.Msg, error) {
	return f(ctx, q)
}

func testQuery() *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	return q
}

func testResponse(q *dns.Msg) *dns.Msg {
	resp := new(dns.Msg)
	resp.SetReply(q)
	return resp
}

func TestHedgedPrimarySuccessDoesNotStartFallback(t *testing.T) {
	var fallbackCalled int32
	h := doh.NewHedged([]doh.Exchanger{
		exchangerFunc(func(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
			return testResponse(q), nil
		}),
		exchangerFunc(func(_ context.Context, _ *dns.Msg) (*dns.Msg, error) {
			atomic.AddInt32(&fallbackCalled, 1)
			return nil, errors.New("fallback should not start")
		}),
	}, time.Second)

	if _, err := h.Exchange(context.Background(), testQuery()); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if got := atomic.LoadInt32(&fallbackCalled); got != 0 {
		t.Fatalf("fallback called %d times, want 0", got)
	}
}

func TestHedgedStartsFallbackAfterDelay(t *testing.T) {
	fallbackStarted := make(chan struct{})
	h := doh.NewHedged([]doh.Exchanger{
		exchangerFunc(func(ctx context.Context, _ *dns.Msg) (*dns.Msg, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
		exchangerFunc(func(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
			close(fallbackStarted)
			return testResponse(q), nil
		}),
	}, 10*time.Millisecond)

	resp, err := h.Exchange(context.Background(), testQuery())
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if resp == nil {
		t.Fatal("Exchange returned nil response")
	}
	select {
	case <-fallbackStarted:
	default:
		t.Fatal("fallback was not started")
	}
}

func TestHedgedStartsNextImmediatelyOnFailure(t *testing.T) {
	h := doh.NewHedged([]doh.Exchanger{
		exchangerFunc(func(_ context.Context, _ *dns.Msg) (*dns.Msg, error) {
			return nil, errors.New("primary failed")
		}),
		exchangerFunc(func(_ context.Context, q *dns.Msg) (*dns.Msg, error) {
			return testResponse(q), nil
		}),
	}, time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := h.Exchange(ctx, testQuery()); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
}

func TestHedgedAllFailJoinsErrors(t *testing.T) {
	h := doh.NewHedged([]doh.Exchanger{
		exchangerFunc(func(_ context.Context, _ *dns.Msg) (*dns.Msg, error) {
			return nil, errors.New("primary failed")
		}),
		exchangerFunc(func(_ context.Context, _ *dns.Msg) (*dns.Msg, error) {
			return nil, errors.New("fallback failed")
		}),
	}, 0)

	_, err := h.Exchange(context.Background(), testQuery())
	if err == nil {
		t.Fatal("expected error")
	}
	if msg := err.Error(); !strings.Contains(msg, "primary failed") || !strings.Contains(msg, "fallback failed") {
		t.Fatalf("joined error %q missing upstream failures", msg)
	}
}

func TestHedgedContextCancelBeforeFallback(t *testing.T) {
	var fallbackCalled int32
	h := doh.NewHedged([]doh.Exchanger{
		exchangerFunc(func(ctx context.Context, _ *dns.Msg) (*dns.Msg, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
		exchangerFunc(func(_ context.Context, _ *dns.Msg) (*dns.Msg, error) {
			atomic.AddInt32(&fallbackCalled, 1)
			return nil, nil
		}),
	}, time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Exchange(ctx, testQuery()); err == nil {
		t.Fatal("expected context error")
	}
	if got := atomic.LoadInt32(&fallbackCalled); got != 0 {
		t.Fatalf("fallback called %d times, want 0", got)
	}
}
