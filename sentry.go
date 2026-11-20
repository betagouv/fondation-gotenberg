package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/gotenberg/gotenberg/v8/pkg/gotenberg"
	"github.com/gotenberg/gotenberg/v8/pkg/modules/api"
	"github.com/labstack/echo/v5"
)

func init() {
	gotenberg.MustRegisterModule(new(Sentry))
}

// Sentry sends to Sentry the requests Gotenberg fails to serve.
type Sentry struct {
	enabled        bool
	disabledReason string
	stopping       atomic.Bool
}

func (s *Sentry) Descriptor() gotenberg.ModuleDescriptor {
	return gotenberg.ModuleDescriptor{
		ID:  "sentry",
		New: func() gotenberg.Module { return new(Sentry) },
	}
}

// Provision only disables the module on a bad DSN so that monitoring never keeps the PDF service from starting.
func (s *Sentry) Provision(*gotenberg.Context) error {
	if os.Getenv("SENTRY_DSN") == "" {
		s.disabledReason = "SENTRY_DSN is not set"
		return nil
	}

	release := os.Getenv("SENTRY_RELEASE")
	if release == "" {
		// Scalingo sets the deployed commit in CONTAINER_VERSION
		release = os.Getenv("CONTAINER_VERSION")
	}

	err := sentry.Init(sentry.ClientOptions{Release: release})
	if err != nil {
		s.disabledReason = fmt.Sprintf("SENTRY_DSN is invalid: %s", err)
		return nil
	}

	s.enabled = true
	return nil
}

// Middlewares captures the 5xx and the 429 sent when the Chromium queue is full.
// Gotenberg turns a panic in a route into a hard timeout error that lands here.
func (s *Sentry) Middlewares() ([]api.Middleware, error) {
	if !s.enabled {
		return nil, nil
	}

	return []api.Middleware{{
		Handler: func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c *echo.Context) error {
				err := next(c)
				if err == nil {
					return nil
				}

				// The client hung up
				if errors.Is(err, context.Canceled) && errors.Is(c.Request().Context().Err(), context.Canceled) {
					return err
				}

				status, _ := api.ParseError(err)
				if status < http.StatusInternalServerError && status != http.StatusTooManyRequests {
					return err
				}

				hub := sentry.CurrentHub().Clone()
				hub.WithScope(func(scope *sentry.Scope) {
					scope.SetRequest(c.Request())
					scope.SetTag("route", c.Request().URL.Path)
					scope.SetTag("status", strconv.Itoa(status))
					// Same correlation_id as the access log line in Scalingo
					if correlationId, ok := c.Get("correlationId").(string); ok {
						scope.SetTag("correlation_id", correlationId)
					}
					hub.CaptureException(err)
				})
				// Gotenberg stops this module while the API still drains requests. Flushing otherwise would delay every error response.
				if s.stopping.Load() {
					hub.Flush(2 * time.Second)
				}

				return err
			}
		},
	}}, nil
}

// Start does nothing. The module is an App only to get Stop.
func (s *Sentry) Start() error {
	return nil
}

func (s *Sentry) StartupMessage() string {
	if !s.enabled {
		return "disabled, " + s.disabledReason
	}
	return "sends errors to Sentry"
}

// Stop sends what is left before Scalingo restarts the container.
func (s *Sentry) Stop(context.Context) error {
	s.stopping.Store(true)
	if s.enabled {
		sentry.Flush(5 * time.Second)
	}
	return nil
}

var (
	_ gotenberg.Module       = (*Sentry)(nil)
	_ gotenberg.Provisioner  = (*Sentry)(nil)
	_ gotenberg.App          = (*Sentry)(nil)
	_ api.MiddlewareProvider = (*Sentry)(nil)
)
