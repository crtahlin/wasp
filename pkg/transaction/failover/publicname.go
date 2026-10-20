// Copyright 2026 The Wasp Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package failover

import (
	"fmt"
	"net/url"
	"strings"
)

// publicName identifies the endpoint at position i (0-based) without what
// a configured URL can carry as a secret: it is the 1-based position and
// the scheme and host, with no user information, path or query. Provider
// URLs often hold an API key in the path or the query, and these names are
// returned by the API (#825).
func publicName(i int, name string) string {
	u, err := url.Parse(name)
	if err != nil || u.Host == "" {
		return fmt.Sprintf("#%d", i+1)
	}
	return fmt.Sprintf("#%d %s://%s", i+1, u.Scheme, u.Host)
}

// redactedError hides the secret parts of an endpoint's URL in the text of
// an error that endpoint returned, such as a transport error that quotes
// the request URL. Unwrap keeps the error itself for errors.Is and
// errors.As.
type redactedError struct {
	err  error
	text string
}

func (e *redactedError) Error() string { return e.text }
func (e *redactedError) Unwrap() error { return e.err }

// redact returns err with every occurrence of the endpoint's configured
// URL, and of its user information, path and query, replaced by its
// public name; nil for nil.
func redact(err error, i int, name string) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	public := publicName(i, name)
	secrets := []string{name}
	if u, perr := url.Parse(name); perr == nil {
		if u.User != nil {
			secrets = append(secrets, u.User.String())
		}
		if u.RawQuery != "" {
			secrets = append(secrets, u.RawQuery)
		}
		if u.Path != "" && u.Path != "/" {
			secrets = append(secrets, u.Path, u.EscapedPath())
		}
	}
	changed := false
	for _, s := range secrets {
		if s != "" && strings.Contains(text, s) {
			replacement := "[redacted]"
			if s == name {
				replacement = public
			}
			text = strings.ReplaceAll(text, s, replacement)
			changed = true
		}
	}
	if !changed {
		return err
	}
	return &redactedError{err: err, text: text}
}
