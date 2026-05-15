package ollama

import (
	"net/http"
	"sync/atomic"
	"testing"
)

// closeTrackingTransport is a thin http.RoundTripper wrapper that counts
// CloseIdleConnections() calls so tests can assert pool resets happened.
type closeTrackingTransport struct {
	inner  http.RoundTripper
	closes atomic.Int32
}

func (t *closeTrackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.inner.RoundTrip(r)
}

func (t *closeTrackingTransport) CloseIdleConnections() {
	t.closes.Add(1)
	type ci interface{ CloseIdleConnections() }
	if c, ok := t.inner.(ci); ok {
		c.CloseIdleConnections()
	}
}

func TestCloseIdle_invokesTransportCloseIdleConnections(t *testing.T) {
	tr := &closeTrackingTransport{inner: http.DefaultTransport}
	c := &Client{BaseURL: "http://example", HTTP: &http.Client{Transport: tr}}

	if got := tr.closes.Load(); got != 0 {
		t.Fatalf("baseline closes = %d, want 0", got)
	}
	c.CloseIdle()
	if got := tr.closes.Load(); got != 1 {
		t.Errorf("after CloseIdle: closes = %d, want 1", got)
	}
	c.CloseIdle()
	if got := tr.closes.Load(); got != 2 {
		t.Errorf("after second CloseIdle: closes = %d, want 2", got)
	}
}

func TestCloseIdle_safeOnDefaultClient(t *testing.T) {
	c := New("http://example")
	// http.DefaultTransport implements CloseIdleConnections; the wrapper
	// in http.Client should call it without panicking.
	c.CloseIdle()
}
