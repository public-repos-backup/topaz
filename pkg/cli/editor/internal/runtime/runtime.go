/*
Copyright 2014 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package runtime

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/rs/zerolog"
)

// ReallyCrash controls the behavior of HandleCrash and defaults to
// true. It's exposed so components can optionally set to false
// to restore prior behavior. This flag is mostly used for tests to validate
// crash conditions.
var ReallyCrash = true

// PanicHandlers is a list of functions which will be invoked when a panic happens.
var PanicHandlers = []func(context.Context, interface{}){logPanic}

// HandleCrash simply catches a crash and logs an error. Meant to be called via
// defer.  Additional context-specific handlers can be provided, and will be
// called in case of panic.  HandleCrash actually crashes, after calling the
// handlers and logging the panic message.
//
// E.g., you can provide one or more additional handlers for something like shutting down go routines gracefully.
//
// TODO(pohly): logcheck:context // HandleCrashWithContext should be used instead of HandleCrash in code which supports contextual logging.
func HandleCrash(additionalHandlers ...func(interface{})) {
	if r := recover(); r != nil {
		additionalHandlersWithContext := make([]func(context.Context, interface{}), len(additionalHandlers))
		for i, handler := range additionalHandlers {
			additionalHandlersWithContext[i] = func(_ context.Context, r interface{}) {
				handler(r)
			}
		}

		handleCrash(context.Background(), r, additionalHandlersWithContext...)
	}
}

// HandleCrashWithContext simply catches a crash and logs an error. Meant to be called via
// defer.  Additional context-specific handlers can be provided, and will be
// called in case of panic.  HandleCrash actually crashes, after calling the
// handlers and logging the panic message.
//
// E.g., you can provide one or more additional handlers for something like shutting down go routines gracefully.
//
// The context is used to determine how to log.
func HandleCrashWithContext(ctx context.Context, additionalHandlers ...func(context.Context, interface{})) {
	if r := recover(); r != nil {
		handleCrash(ctx, r, additionalHandlers...)
	}
}

// handleCrash is the common implementation of HandleCrash and HandleCrash.
// Having those call a common implementation ensures that the stack depth
// is the same regardless through which path the handlers get invoked.
func handleCrash(ctx context.Context, r any, additionalHandlers ...func(context.Context, interface{})) {
	for _, fn := range PanicHandlers {
		fn(ctx, r)
	}
	for _, fn := range additionalHandlers {
		fn(ctx, r)
	}
	if ReallyCrash {
		// Actually proceed to panic.
		panic(r)
	}
}

// logPanic logs the caller tree when a panic occurs (except in the special case of http.ErrAbortHandler).
func logPanic(ctx context.Context, r interface{}) {
	if errors.Is(r.(error), http.ErrAbortHandler) {
		// honor the http.ErrAbortHandler sentinel panic value:
		//   ErrAbortHandler is a sentinel panic value to abort a handler.
		//   While any panic from ServeHTTP aborts the response to the client,
		//   panicking with ErrAbortHandler also suppresses logging of a stack trace to the server's error log.
		return
	}

	// Same as stdlib http server code. Manually allocate stack trace buffer size
	// to prevent excessively large logs
	const size = 64 << 10
	stacktrace := make([]byte, size)
	stacktrace = stacktrace[:runtime.Stack(stacktrace, false)]

	// We don't really know how many call frames to skip because the Go
	// panic handler is between us and the code where the panic occurred.
	// If it's one function (as in Go 1.21), then skipping four levels
	// gets us to the function which called the `defer HandleCrashWithontext(...)`.
	logger := zerolog.Ctx(ctx)
	// For backwards compatibility, conversion to string
	// is handled here instead of defering to the logging
	// backend.
	if _, ok := r.(string); ok {
		logger.Error().Any("panic", r).Str("stacktrace", string(stacktrace)).Msg("panic")
	} else {
		logger.Error().Str("panic", fmt.Sprintf("%v", r)).Str("panicGoValue", fmt.Sprintf("%#v", r)).Str("stacktrace", string(stacktrace)).Msg("panic")
	}
}

// ErrorHandlers is a list of functions which will be invoked when a nonreturnable
// error occurs.
// TODO(lavalamp): for testability, this and the below HandleError function
// should be packaged up into a testable and reusable object.
var ErrorHandlers = []ErrorHandler{
	logError,
	func(_ context.Context, _ error, _ string, _ ...interface{}) {
		(&rudimentaryErrorBackoff{
			lastErrorTime: time.Now(),
			// 1ms was the number folks were able to stomach as a global rate limit.
			// If you need to log errors more than 1000 times a second you
			// should probably consider fixing your code instead. :)
			minPeriod: time.Millisecond,
		}).OnError()
	},
}

type ErrorHandler func(ctx context.Context, err error, msg string, keysAndValues ...interface{})

// HandlerError is a method to invoke when a non-user facing piece of code cannot
// return an error and needs to indicate it has been ignored. Invoking this method
// is preferable to logging the error - the default behavior is to log but the
// errors may be sent to a remote server for analysis.
//
// TODO(pohly): logcheck:context // HandleErrorWithContext should be used instead of HandleError in code which supports contextual logging.
func HandleError(err error) {
	// this is sometimes called with a nil error.  We probably shouldn't fail and should do nothing instead
	if err == nil {
		return
	}

	handleError(context.Background(), err, "Unhandled Error")
}

// HandlerErrorWithContext is a method to invoke when a non-user facing piece of code cannot
// return an error and needs to indicate it has been ignored. Invoking this method
// is preferable to logging the error - the default behavior is to log but the
// errors may be sent to a remote server for analysis. The context is used to
// determine how to log the error.
//
// If contextual logging is enabled, the default log output is equivalent to
//
//	logr.FromContext(ctx).WithName("UnhandledError").Error(err, msg, keysAndValues...)
//
// Without contextual logging, it is equivalent to:
//
//	klog.ErrorS(err, msg, keysAndValues...)
//
// In contrast to HandleError, passing nil for the error is still going to
// trigger a log entry. Don't construct a new error or wrap an error
// with errors.Errorf. Instead, add additional information via the message
// and key/value pairs.
//
// This variant should be used instead of HandleError because it supports
// structured, contextual logging.
func HandleErrorWithContext(ctx context.Context, err error, msg string, keysAndValues ...interface{}) {
	handleError(ctx, err, msg, keysAndValues...)
}

// handleError is the common implementation of HandleError and HandleErrorWithContext.
// Using this common implementation ensures that the stack depth
// is the same regardless through which path the handlers get invoked.
func handleError(ctx context.Context, err error, msg string, keysAndValues ...interface{}) {
	for _, fn := range ErrorHandlers {
		fn(ctx, err, msg, keysAndValues...)
	}
}

// logError prints an error with the call stack of the location it was reported.
// It expects to be called as <caller> -> HandleError[WithContext] -> handleError -> logError.
func logError(ctx context.Context, err error, msg string, keysAndValues ...interface{}) {
	logger := zerolog.Ctx(ctx)
	logger.Error().Err(err).Str("msg", msg).Any("kv", keysAndValues).Msg("UnhandledError")
}

type rudimentaryErrorBackoff struct {
	minPeriod time.Duration // immutable
	// TODO(lavalamp): use the clock for testability. Need to move that
	// package for that to be accessible here.
	lastErrorTimeLock sync.Mutex
	lastErrorTime     time.Time
}

// OnError will block if it is called more often than the embedded period time.
// This will prevent overly tight hot error loops.
func (r *rudimentaryErrorBackoff) OnError() {
	now := time.Now() // start the timer before acquiring the lock
	r.lastErrorTimeLock.Lock()
	d := now.Sub(r.lastErrorTime)
	r.lastErrorTime = time.Now()
	r.lastErrorTimeLock.Unlock()

	// Do not sleep with the lock held because that causes all callers of HandleError to block.
	// We only want the current goroutine to block.
	// A negative or zero duration causes time.Sleep to return immediately.
	// If the time moves backwards for any reason, do nothing.
	time.Sleep(r.minPeriod - d)
}

// GetCaller returns the caller of the function that calls it.
func GetCaller() string {
	var pc [1]uintptr
	runtime.Callers(3, pc[:])
	f := runtime.FuncForPC(pc[0])
	if f == nil {
		return "Unable to find caller"
	}
	return f.Name()
}

// RecoverFromPanic replaces the specified error with an error containing the
// original error, and  the call tree when a panic occurs. This enables error
// handlers to handle errors and panics the same way.
func RecoverFromPanic(err *error) {
	if r := recover(); r != nil {
		// Same as stdlib http server code. Manually allocate stack trace buffer size
		// to prevent excessively large logs
		const size = 64 << 10
		stacktrace := make([]byte, size)
		stacktrace = stacktrace[:runtime.Stack(stacktrace, false)]

		*err = errors.Errorf(
			"recovered from panic %q. (err=%v) Call stack:\n%s",
			r,
			*err,
			stacktrace)
	}
}

// Must panics on non-nil errors. Useful to handling programmer level errors.
func Must(err error) {
	if err != nil {
		panic(err)
	}
}
