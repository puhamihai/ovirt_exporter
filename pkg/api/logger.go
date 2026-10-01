// SPDX-License-Identifier: MIT

package api

import "log"

// Logger logs messages
type Logger interface {
	// Infof logs message with info severity
	Infof(format string, args ...interface{})
	// Debugf logs message with debug severity
	Debugf(format string, args ...interface{})
	// Errorf logs message with error severity
	Errorf(format string, args ...interface{})
}

// defaultLogger is the default implementation of Logger using golangs log
// package; debug messages are dropped
type defaultLogger struct{}

// Infof implements Logger interface
func (l *defaultLogger) Infof(format string, args ...interface{}) {
	log.Printf(format, args...)
}

// Debugf implements Logger interface
func (l *defaultLogger) Debugf(format string, args ...interface{}) {}

// Errorf implements Logger interface
func (l *defaultLogger) Errorf(format string, args ...interface{}) {
	log.Printf(format, args...)
}
