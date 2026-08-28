// Copyright 2022 The Swarm Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package log

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sync"

	"github.com/hashicorp/go-multierror"
)

// defaults specifies the default global options for log
// package which every new logger will inherit on its creation.
var defaults = struct {
	pin       sync.Once // pin pins the options and formatter settings.
	options   *Options
	formatter *formatter
}{
	options: &Options{
		sink: os.Stderr,
		// Buffered by default. This deliberately departs from the convention
		// that a new setting keeps the previous behaviour: the previous
		// behaviour is the defect in issue #156, so defaulting to it would
		// leave nodes able to go permanently deaf and make the fix opt-in for
		// whoever does not know they need it. WithSynchronousSink restores it.
		sinkBuffer: DefaultSinkBuffer,
		verbosity:  VerbosityDebug,
		fmtOptions: fmtOptions{
			timestampLayout: "2006-01-02 15:04:05.000000",
			maxLogDepth:     16,
		},
	},
}

// ModifyDefaults modifies the global default options for this log package
// that each new logger inherits when it is created. The default values can
// be modified only once, so further calls to this function will be ignored.
// This function should be called before the first call to the NewLogger
// factory constructor, otherwise it will have no effect.
func ModifyDefaults(opts ...Option) {
	defaults.pin.Do(func() {
		for _, modify := range opts {
			modify(defaults.options)
		}
		defaults.formatter = newFormatter(defaults.options.fmtOptions)
	})
}

// loggers is the central register for Logger instances.
var loggers = new(sync.Map)

// NewLogger is a factory constructor which returns a new logger instance
// based on the given name. If such an instance already exists in the
// logger registry, then this existing instance is returned instead.
// The given options take precedence over the default options set
// by the ModifyDefaults function.
func NewLogger(name string, opts ...Option) Logger {
	// Pin the default settings if
	// they are not already pinned.
	ModifyDefaults()

	options := *defaults.options
	for _, modify := range opts {
		modify(&options)
	}

	if options.sink == io.Discard {
		return Noop
	}

	formatter := defaults.formatter
	if options.fmtOptions != defaults.options.fmtOptions {
		formatter = newFormatter(options.fmtOptions)
	}

	// Deduplicate on the sink the CALLER gave us, before any wrapping below.
	//
	// hash folds the sink's pointer into the key. Wrapping first would hand
	// every call a fresh pointer, so no logger would ever be found in the
	// registry and each of the thirty-odd WithName(...).Register() sites would
	// get a logger of its own, each with its own drain goroutine.
	key := hash(name, 0, "", options.sink)
	val, ok := loggers.Load(key)
	if ok {
		return val.(*logger)
	}

	// Writing to the sink must not be able to block the goroutine that logs.
	// See asyncsink.go and issue #156. asyncSinkFor keeps one wrapper per
	// underlying writer, so a process with one os.Stdout has one drain
	// goroutine however many loggers it builds.
	sink := asyncSinkFor(options.sink, options.sinkBuffer, options.sinkBufferForced)

	l := &logger{
		formatter:  formatter,
		verbosity:  options.verbosity,
		sink:       sink,
		levelHooks: options.levelHooks,
		metrics:    options.logMetrics,
	}
	l.builder = &builder{
		l:        l,
		names:    []string{name},
		namesStr: name,
	}
	return l
}

// SetVerbosity sets the level
// of verbosity of the given logger.
func SetVerbosity(l Logger, v Level) error {
	bl := l.(*logger)
	switch newLvl, maxValue := v.get(), Level(bl.v); {
	case newLvl == VerbosityAll:
		bl.setVerbosity(maxValue)
	case newLvl > maxValue:
		return fmt.Errorf("maximum verbosity %d exceeded for logger: %s", bl.v, bl.id)
	default:
		bl.setVerbosity(newLvl)
	}
	return nil
}

// SetVerbosityByExp sets all loggers to the given
// verbosity level v that match the given expression
// e, which can be a logger id or a regular expression.
// An error is returned if e fails to compile.
func SetVerbosityByExp(e string, v Level) error {
	val, ok := loggers.Load(e)
	if ok {
		val.(*logger).setVerbosity(v)
		return nil
	}

	rex, err := regexp.Compile(e)
	if err != nil {
		return err
	}

	var merr *multierror.Error
	loggers.Range(func(key, val any) bool {
		if rex.MatchString(key.(string)) {
			merr = multierror.Append(merr, SetVerbosity(val.(*logger), v))
		}
		return true
	})
	return merr.ErrorOrNil()
}

// RegistryIterate iterates through all registered loggers.
func RegistryIterate(fn func(id, path string, verbosity Level, v uint) (next bool)) {
	loggers.Range(func(_, val any) bool {
		l := val.(*logger)
		return fn(l.id, l.namesStr, l.verbosity.get(), l.v)
	})
}
