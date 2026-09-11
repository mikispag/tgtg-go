package tgtg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCanceledLoginKeepsPINBoundToAttempt(t *testing.T) {
	for _, changeEmail := range []bool{false, true} {
		t.Run(fmt.Sprintf("change_email=%t", changeEmail), func(t *testing.T) {
			var emailCalls, pinCalls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/" + AuthByEmailEndpoint:
					id := fmt.Sprintf("id%d", emailCalls.Add(1))
					_ = json.NewEncoder(w).Encode(map[string]string{"state": "WAIT", "polling_id": id})
				case "/" + AuthByRequestPinEndpoint:
					var body struct {
						PollingID  string `json:"request_polling_id"`
						RequestPIN string `json:"request_pin"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body.RequestPIN != "pin"+body.PollingID {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"error":"PIN belongs to another attempt"}`)
						return
					}
					_, _ = io.WriteString(w, `{"access_token":"access","refresh_token":"refresh"}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(s.Close)
			entered, release := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			c := New(Config{URL: s.URL + "/", DataDomeSDKURL: s.URL + "/sdk", UserAgent: "ua", Output: io.Discard, Email: "first@example.test", PinReader: func() (string, error) {
				call := pinCalls.Add(1)
				if call == 1 {
					close(entered)
					<-release
				}
				return fmt.Sprintf("pinid%d", call), nil
			}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- c.Login(ctx) }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("PIN callback did not start")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("first Login = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled login did not return")
			}
			if changeEmail {
				c.Email = "second@example.test"
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				err := c.Login(ctx)
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("login while old reader is pending = %v", err)
				}
				if got := emailCalls.Load(); got != 1 {
					t.Errorf("new authentication started before old PIN input was discarded: %d requests", got)
				}
			}
			close(release)
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := c.Login(ctx); err != nil {
				t.Fatalf("subsequent Login submitted a PIN for the wrong attempt: %v", err)
			}
			want := int32(1)
			if changeEmail {
				want = 2
			}
			if got := emailCalls.Load(); got != want {
				t.Errorf("email authentication requests = %d, want %d", got, want)
			}
			if got := pinCalls.Load(); got != want {
				t.Errorf("PIN callbacks = %d, want %d", got, want)
			}
		})
	}
}
