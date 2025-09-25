package zyte

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math/rand"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Transport implements a [http.RoundTripper] using the Zyte API.
type Transport struct {
	// APIKey is used to authenticate with Zyte. To use x402, handle it in Next.
	APIKey string

	// Retry controls retry behaviour for unsuccessful requests. If nil,
	// unsuccessful requests are not retried. It is not called for rate-limiting
	// responses, which are retried infinitely until the context is canceled.
	Retry RetryFunc

	// Limit is called for cost-incurring responses and before making requests,
	// If nil, requests are unlimited.
	Limit LimitFunc

	// FollowRedirect controls whether to follow redirects internally. This is
	// not how a [http.RoundTripper] is expected to behave, but costs less. If
	// false, beware of redirect loops.
	FollowRedirect bool

	// Next is used for making Zyte API requests. If nil,
	// [http.DefaultTransport] is used.
	Next http.RoundTripper
}

// RetryFunc is called with the number of retries attempted and the last
// response status for ban responses. It should delay and return true. It should
// return false to prevent a retry. It should also return false if ctx is
// canceled.
type RetryFunc func(ctx context.Context, tries, code int) bool

// LimitFunc is called to add n to the number of requests made (may be zero),
// and returns an error if the limit has been reached or exceeded.
type LimitFunc func(n int) error

// FixedLimit allows a fixed number of requests.
func FixedLimit(limit int) LimitFunc {
	var requests int
	return func(n int) error {
		if limit != -1 && requests >= limit {
			return fmt.Errorf("limit %d reached", limit)
		}
		requests++
		return nil
	}
}

// RetryLimit returns a RetryFunc with a fixed retry limit, using the
// recommended retry interval.
func RetryLimit(n int) RetryFunc {
	return func(ctx context.Context, tries, code int) bool {
		if n != -1 && tries >= n {
			return false
		}
		var maxDelay time.Duration
		switch tries {
		case 0:
			maxDelay = time.Second * 9
		case 1:
			maxDelay = time.Second * 11
		case 2:
			maxDelay = time.Second * 15
		case 3:
			maxDelay = time.Second * 23
		case 4:
			maxDelay = time.Second * 39
		default:
			maxDelay = time.Second * 62
		}
		minDelay := time.Second * 3
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Duration(rand.Int63n(int64(maxDelay-minDelay))) + minDelay):
			return true
		}
	}
}

// Error is an error from the Zyte API.
type Error struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func (z Error) Error() string {
	var b strings.Builder
	b.WriteString("zyte api error")
	if z.Type != "" {
		b.WriteString(": ")
		b.WriteString(z.Type)
	}
	if z.Status != 0 {
		if z.Type != "" {
			b.WriteString(": ")
		} else {
			b.WriteByte('(')
		}
		b.WriteString(strconv.Itoa(z.Status))
		if z.Type == "" {
			b.WriteByte(')')
		}
	}
	if z.Title != "" {
		b.WriteString(": ")
		b.WriteString(z.Title)
	}
	if z.Detail != "" {
		b.WriteString(": ")
		b.WriteString(z.Detail)
	}
	return b.String()
}

func (z Error) Is(o error) bool {
	if o, ok := o.(Error); ok {
		if z.Type != "" && o.Type != "" {
			return z.Type == o.Type
		}
		if z.Status != 0 && o.Status != 0 {
			return z.Status == o.Status
		}
	}
	return false
}

type requestKey struct{}

var _ http.RoundTripper = (*Transport)(nil)

func (z *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	if ctx.Value(requestKey{}) != nil {
		return nil, fmt.Errorf("recursive %T", z)
	}
	ctx = context.WithValue(ctx, requestKey{}, true)

	zreqObj := map[string]any{
		"httpResponseBody":    true,
		"httpResponseHeaders": true,
		"url":                 req.URL.String(),
		"followRedirect":      z.FollowRedirect,
	}
	if req.Method != http.MethodGet {
		zreqObj["httpRequestMethod"] = req.Method
	}
	if req.Body != nil {
		defer req.Body.Close()
		buf, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(buf)), nil
		}
		req.Body, _ = req.GetBody()
		zreqObj["httpRequestBody"] = buf // base64
	}
	if cookies := req.Cookies(); len(cookies) != 0 {
		return nil, fmt.Errorf("zyte: cookies not supported")
	}
	if len(req.Header) != 0 {
		var obj []any
		for _, k := range slices.Sorted(maps.Keys(req.Header)) {
			if v := req.Header[k]; len(v) != 0 {
				obj = append(obj, map[string]any{
					"name":  k,
					"value": strings.Join(v, ","),
				})
			}
		}
		zreqObj["customHttpRequestHeaders"] = obj
	}
	zreqBuf, err := json.Marshal(zreqObj)
	if err != nil {
		return nil, fmt.Errorf("zyte: prepare request: %w", err)
	}

	var zrespObj struct {
		URL                 string  `json:"url"`
		StatusCode          int     `json:"statusCode"`
		HTTPResponseBody    *[]byte `json:"httpResponseBody"` // base64
		HTTPResponseHeaders *[]struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"httpResponseHeaders"`
	}
	var tries int
	for {
		if z.Limit != nil {
			if err := z.Limit(0); err != nil {
				return nil, fmt.Errorf("zyte: request limit reached: %w", err)
			}
		}

		zreq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.zyte.com/v1/extract", bytes.NewReader(zreqBuf))
		if err != nil {
			return nil, fmt.Errorf("zyte: prepare request: %w", err)
		}
		if z.APIKey != "" {
			zreq.SetBasicAuth(z.APIKey, "")
		}

		zresp, err := cmp.Or(z.Next, http.DefaultTransport).RoundTrip(zreq)
		if err != nil {
			return nil, err
		}

		// https://docs.zyte.com/zyte-api/usage/errors.html#successful-responses
		if zresp.StatusCode/100 == 2 {
			if z.Limit != nil {
				z.Limit(1) // 2xx response is paid (https://docs.zyte.com/zyte-api/pricing.html)
			}
		}

		// https://docs.zyte.com/zyte-api/usage/errors.html#rate-limiting-responses
		if zresp.StatusCode == 429 || zresp.StatusCode == 503 {
			s := zresp.Header.Get("Retry-After")
			if s == "" {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Second * 30):
					continue
				}
			}
			if retryAfter, err := http.ParseTime(s); err == nil {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Until(retryAfter)):
					continue
				}
			}
			if retryAfter, err := strconv.Atoi(s); err == nil {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Second * time.Duration(retryAfter)):
					continue
				}
			}
			return nil, fmt.Errorf("zyte: failed to parse rate-limit retry-after %q", s)
		}

		// https://docs.zyte.com/zyte-api/usage/errors.html#ban-responses
		// https://docs.zyte.com/zyte-api/usage/errors.html#permanent-download-errors
		if zresp.StatusCode == 500 || zresp.StatusCode == 520 || zresp.StatusCode == 521 {
			if z.Retry != nil && z.Retry(ctx, tries, zresp.StatusCode) {
				tries++
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("zyte: retry limit reached (try %d, status %d)", tries, zresp.StatusCode)
		}

		// https://docs.zyte.com/zyte-api/usage/errors.html#unsuccessful-responses
		if zresp.StatusCode != 200 {
			buf, err := io.ReadAll(zresp.Body)
			if err != nil {
				return nil, fmt.Errorf("zyte: failed to parse error %d response: %w", zresp.StatusCode, err)
			}
			var zerr Error
			if err := json.Unmarshal(buf, &zerr); err != nil {
				if len(buf) > 1024 {
					buf = buf[:1024]
				}
				return nil, fmt.Errorf("zyte: failed to parse error %d response %q: %w", zresp.StatusCode, string(buf), err)
			}
			return nil, zerr
		}

		// https://docs.zyte.com/zyte-api/usage/errors.html#successful-responses
		if err := json.NewDecoder(zresp.Body).Decode(&zrespObj); err != nil {
			return nil, fmt.Errorf("zyte: failed to parse response: %w", err)
		}
		if zrespObj.StatusCode == 0 || zrespObj.URL == "" || zrespObj.HTTPResponseBody == nil || zrespObj.HTTPResponseHeaders == nil {
			return nil, fmt.Errorf("zyte: failed to parse response: missing fields")
		}
		break
	}

	freq := req.Clone(ctx)
	if ru, err := url.Parse(zrespObj.URL); err != nil {
		return nil, fmt.Errorf("parse response url: %w", err)
	} else {
		freq.URL = ru
		freq.Host = ru.Host
	}
	fresp := &http.Response{
		Status:     http.StatusText(zrespObj.StatusCode),
		StatusCode: zrespObj.StatusCode,
		Proto:      req.Proto,
		ProtoMajor: req.ProtoMajor,
		ProtoMinor: req.ProtoMinor,
		Request:    freq,
		Header:     http.Header{},
		Close:      true,
	}
	for _, h := range *zrespObj.HTTPResponseHeaders {
		fresp.Header.Add(h.Name, h.Value)
	}
	if buf := *zrespObj.HTTPResponseBody; len(buf) != 0 {
		if fresp.Header.Get("Content-Encoding") != "" {
			fresp.ContentLength = -1
			fresp.Uncompressed = true
			fresp.Header.Del("Content-Encoding")
			fresp.Header.Del("Content-Length")
		} else {
			fresp.ContentLength = int64(len(buf))
		}
		fresp.Body = io.NopCloser(bytes.NewReader(buf))
	}
	return fresp, nil
}
