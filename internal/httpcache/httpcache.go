package httpcache

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Transport caches HTTP responses indefinitely based on a URL and an optional
// category. It supports redacting sensitive headers/parameters.
type Transport struct {
	// Path is the path to store cached requests at.
	Path string

	// Fallback allows pages from any category to be used.
	Fallback bool

	// RequestRedactor redacts requests for storage.
	RequestRedactor RequestRedactor

	// ResponseRedactor redacts responses.
	ResponseRedactor ResponseRedactor

	// Next is the transport to use for making requests. If nil, only cached
	// responses are used.
	Next http.RoundTripper
}

type categoryKey struct{}

func CategoryContext(ctx context.Context, category string) context.Context {
	return context.WithValue(ctx, categoryKey{}, category)
}

func WithCategory(r *http.Request, category string) *http.Request {
	return r.WithContext(CategoryContext(r.Context(), category))
}

func contextCategory(ctx context.Context) string {
	if v, ok := ctx.Value(categoryKey{}).(string); ok && v != "" {
		return v
	}
	return "req"
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return nil, fmt.Errorf("httpcache: unsupported method %s", req.Method)
	}

	var cacheName, cacheSuffix string
	if t.Path != "" {
		s := sha1.Sum([]byte(req.URL.String()))
		cacheSuffix = "-" + hex.EncodeToString(s[:])
		cacheName = filepath.Join(t.Path, contextCategory(req.Context())+cacheSuffix)
	}

	var resp *http.Response
	if cacheName != "" {
		buf, err := os.ReadFile(cacheName)
		if t.Fallback && errors.Is(err, fs.ErrNotExist) {
			ds, err1 := os.ReadDir(t.Path)
			if err1 != nil {
				return nil, fmt.Errorf("httpcache: scan cache: %w", err)
			}
			for _, d := range ds {
				if strings.HasSuffix(d.Name(), cacheSuffix) {
					buf, err = os.ReadFile(filepath.Join(t.Path, d.Name()))
					if err != nil {
						return nil, fmt.Errorf("httpcache: read fallback cached response: %w", err)
					}
					break
				}
			}
		}
		if err == nil {
			r := bufio.NewReader(bytes.NewReader(buf))

			req, err := http.ReadRequest(r)
			if err != nil {
				return nil, fmt.Errorf("httpcache: read cached response: %w", err)
			}
			req.URL.Scheme = "https"
			req.URL.Host = req.Host

			resp, err = http.ReadResponse(r, req)
			if err != nil {
				return nil, fmt.Errorf("httpcache: read cached response: %w", err)
			}
			return resp, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("httpcache: read cached response: %w", err)
		}
	}

	if t.Next == nil {
		if cacheName == "" {
			return nil, fmt.Errorf("httpcache: fetch disabled")
		}
		return nil, fmt.Errorf("httpcache: fetch disabled, response not in cache (%s)", cacheName)
	}

	redacted := req
	if t.RequestRedactor != nil {
		redacted = t.RequestRedactor.Redact(req)
	}

	resp, err := t.Next.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if t.ResponseRedactor != nil {
		resp = t.ResponseRedactor.RedactResponse(resp)
	}

	reqbuf, err := httputil.DumpRequest(redacted, true)
	if err != nil {
		return nil, err
	}

	respbuf, err := httputil.DumpResponse(resp, true)
	if err != nil {
		return nil, err
	}

	if cacheName != "" {
		if err := os.WriteFile(cacheName, slices.Concat(reqbuf, respbuf), 0666); err != nil {
			return nil, fmt.Errorf("httpcache: write cached response: %w", err)
		}
	}
	return resp, nil
}

// Purge purges the specified categories from the cache.
func Purge(path string, categories ...string) error {
	ds, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	for _, d := range ds {
		if d.IsDir() {
			continue
		}
		if !slices.ContainsFunc(categories, func(category string) bool {
			return strings.HasPrefix(d.Name(), category+"-")
		}) {
			continue
		}
		if err := os.Remove(filepath.Join(path, d.Name())); err != nil {
			return err
		}
	}
	return nil
}

// RequestRedactor returns a redacted copy of a request. If fields are modified,
// they must be copied.
type RequestRedactor interface {
	Redact(req *http.Request) *http.Request
}

// ResponseRedactor redacts a response in-place.
type ResponseRedactor interface {
	RedactResponse(r *http.Response) *http.Response
}

type Redactor struct {
	req  []func(*http.Request)
	resp []func(*http.Response)
}

// RedactRequestHeader redacts headers with the specified name, optionally with
// the specified number of hashed characters.
func (r *Redactor) RedactRequestHeader(name string, hashed int) {
	r.req = append(r.req, func(req *http.Request) {
		for n, vs := range req.Header {
			if textproto.CanonicalMIMEHeaderKey(n) == textproto.CanonicalMIMEHeaderKey(name) {
				for i, v := range vs {
					req.Header[n][i] = redactHashed(v, hashed)
				}
			}
		}
	})
}

// RedactRequestURLParam redacts URL parameters with the specified
// case-sensitive name, optionally with the specified number of hashed
// characters.
func (r *Redactor) RedactRequestURLParam(name string, hashed int) {
	r.req = append(r.req, func(req *http.Request) {
		var q strings.Builder
		for x := range strings.SplitSeq(req.URL.RawQuery, "&") {
			if q.Len() != 0 {
				q.WriteByte('&')
			}
			k, v, eq := strings.Cut(x, "=")
			if ku, err := url.QueryUnescape(k); err == nil && ku == name {
				vu, err := url.QueryUnescape(v)
				if err == nil {
					vu = v
				}
				v = redactHashed(vu, hashed)
			}
			q.WriteString(k)
			if eq {
				q.WriteByte('=')
				q.WriteString(v)
			}
		}
		req.URL.RawQuery = q.String()
	})
}

func redactHashed(v string, hashed int) string {
	if hashed != 0 {
		h := sha1.Sum([]byte(v))
		return "redacted-" + hex.EncodeToString(h[:min(hashed, sha1.Size)])
	}
	return "redacted"
}

func (x *Redactor) Redact(r *http.Request) *http.Request {
	r2 := *r
	u2 := *r.URL
	r2.URL = &u2
	r2.Header = r.Header.Clone()
	r = &r2
	for _, fn := range x.req {
		fn(r)
	}
	return r
}

func (x *Redactor) RedactResponse(r *http.Response) *http.Response {
	for _, fn := range x.resp {
		fn(r)
	}
	return r
}
