package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"maps"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/time/rate"
)

var (
	cacheOnly    = flag.Bool("cache-only", false, "don't attempt to parse data from pages")
	noFetch      = flag.Bool("no-fetch", false, "don't fetch pages not in cache")
	cacheDir     = flag.String("cache-dir", "", "cache pages in the specified directory")
	nominatim    = flag.String("nominatim", "https://nominatim.geocoding.ai", "nominatim base url")
	nominatimQPS = flag.Float64("nominatim-qps", 1, "maximum nominatim queries per second")
	placeListing = flag.String("place-listing", "https://ottawa.ca/en/recreation-and-parks/facilities/place-listing", "place listing url to start scraping from")
)

var nominatimLimit = sync.OnceValue(func() *rate.Limiter {
	return rate.NewLimiter(rate.Limit(*nominatimQPS), 1)
})

func init() {
	http.DefaultClient.Jar, _ = cookiejar.New(nil)
}

func main() {
	flag.Parse()

	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	if *cacheDir != "" {
		slog.Info("using cache dir", "path", cacheDir)
		if err := os.Mkdir(*cacheDir, 0777); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create cache dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(*cacheDir, ".gitattributes"), []byte("* -text\n"), 0666); err != nil { // no line ending conversions
			return fmt.Errorf("write cache dir gitattributes: %w", err)
		}
	}

	cur := *placeListing

	for cur != "" {
		doc, err := fetchPage(ctx, cur)
		if err != nil {
			return err
		}

		content, err := scrapeMainContentBlock(doc)
		if err != nil {
			return err
		}

		nextURL, err := scrapePagerNext(doc, content)
		if err != nil {
			return err
		}

		if err := scrapePlaceListings(doc, content, func(u *url.URL, name, address string) error {
			lng, lat, hasLngLat, err := geocode(ctx, address)
			if err != nil {
				return fmt.Errorf("geocode: %w", err)
			}
			doc, err := fetchPage(ctx, u.String())
			if err != nil {
				return err
			}
			slog.Info("got place", "name", name, "address", address, "lng", lng, "lat", lat)

			if *cacheOnly {
				return nil
			}

			_ = doc
			_ = hasLngLat

			return nil
		}); err != nil {
			return err
		}

		if nextURL == nil {
			break
		}
		cur = nextURL.String()
	}
	return nil
}

func geocode(ctx context.Context, addr string) (lng, lat float64, ok bool, err error) {
	resp, err := fetchNominatim(ctx, &url.URL{
		Path: "search",
		RawQuery: url.Values{
			"format": {"geocodejson"},
			"q":      {addr},
		}.Encode(),
	})
	if err != nil {
		return 0, 0, false, err
	}
	defer resp.Body.Close()

	var obj struct {
		Type     string
		Features []struct {
			Type     string
			Geometry struct {
				Type        string
				Coordinates []float64
			}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil {
		return 0, 0, false, fmt.Errorf("decode geocodejson: %w", err)
	}
	if obj.Type != "FeatureCollection" {
		return 0, 0, false, fmt.Errorf("decode geocodejson: wrong type %q", obj.Type)
	}
	for _, f := range obj.Features {
		if f.Type == "Feature" {
			if f.Geometry.Type != "Point" {
				return 0, 0, false, fmt.Errorf("decode geocodejson: wrong feature geometry type %q", f.Geometry.Type)
			}
			if len(f.Geometry.Coordinates) != 2 {
				return 0, 0, false, fmt.Errorf("decode geocodejson: wrong feature geometry coordinates length %d", len(f.Geometry.Coordinates))
			}
			return f.Geometry.Coordinates[0], f.Geometry.Coordinates[1], true, nil
		}
	}
	return 0, 0, false, nil
}

func fetchPage(ctx context.Context, u string) (*goquery.Document, error) {
	slog.Info("fetch page", "url", u)

	resp, err := fetch(ctx, u, "page", u, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}
	doc.Url = resp.Request.URL

	return doc, nil
}

func fetchNominatim(ctx context.Context, r *url.URL) (*http.Response, error) {
	slog.Info("fetch nominatim", "url", r.String())

	u, err := url.Parse(*nominatim)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	u = u.ResolveReference(r)
	maps.Copy(q, r.Query()) // keep orig query params (e.g., for apikey)
	u.RawQuery = q.Encode()

	return fetch(ctx, u.String(), "nominatim", r.String(), nominatimLimit())
}

func fetch(ctx context.Context, u string, cacheType, cacheKey string, limiter *rate.Limiter) (*http.Response, error) {
	var cacheName string
	if *cacheDir != "" {
		s := sha1.Sum([]byte(cacheKey))
		cacheName = filepath.Join(*cacheDir, cacheType+"-"+hex.EncodeToString(s[:]))
	}

	var resp *http.Response
	if cacheName != "" {
		if buf, err := os.ReadFile(cacheName); err == nil {
			r := bufio.NewReader(bytes.NewReader(buf))

			req, err := http.ReadRequest(r)
			if err != nil {
				return nil, fmt.Errorf("read cached response: %w", err)
			}
			req.URL.Scheme = "https"
			req.URL.Host = req.Host

			resp, err = http.ReadResponse(r, req)
			if err != nil {
				return nil, fmt.Errorf("read cached response: %w", err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read cached response: %w", err)
		} else if *noFetch {
			return nil, fmt.Errorf("get %q: fetch disabled, response not in cache", u)
		}
	}
	if resp == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}

		req.Header.Set("User-Agent", "ottawa-rec-scraper-bot/0.1 (dev)")

		if limiter != nil {
			if err := limiter.Wait(ctx); err != nil {
				return nil, err
			}
		}

		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if cacheName != "" {
			reqbuf, err := httputil.DumpRequest(resp.Request, true)
			if err != nil {
				return nil, err
			}

			respbuf, err := httputil.DumpResponse(resp, true)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(cacheName, slices.Concat(reqbuf, respbuf), 0666); err != nil {
				return nil, fmt.Errorf("write cached response: %w", err)
			}
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("response status %d", resp.StatusCode)
	}
	return resp, nil
}

// resolve resolves the href from an element against the document.
func resolve(d *goquery.Document, n *goquery.Selection) (*url.URL, error) {
	var err error
	u := d.Url
	if base, _ := d.Find("base").Attr("href"); base != "" {
		if u, err = u.Parse(base); err != nil {
			return nil, fmt.Errorf("parse base href %q: %w", base, err)
		}
	}
	if href, ok := n.Attr("href"); ok {
		if u, err = u.Parse(href); err != nil {
			return nil, fmt.Errorf("parse href %q: %w", href, err)
		}
	} else {
		return nil, fmt.Errorf("no href specified")
	}
	return u, nil
}

func findOne(s *goquery.Selection, sel, what string) (*goquery.Selection, error) {
	if s == nil {
		return nil, fmt.Errorf("%s (%#q) not found", what, sel)
	}

	s = s.Find(sel)
	if n := s.Length(); n == 0 {
		return nil, fmt.Errorf("%s (%#q) not found", what, sel)
	} else if n > 1 {
		return nil, fmt.Errorf("multiple (%d) %s (%#q) found", n, what, sel)
	}
	return s, nil
}

// scrapeMainContentBlock extracts the main content block from a City of Ottawa
// page.
func scrapeMainContentBlock(doc *goquery.Document) (*goquery.Selection, error) {
	return findOne(doc.Selection, `#block-mainpagecontent`, "main page content wrapper")
}

// scrapePagerNext extracts the next paginated URL from a section of a City of
// Ottawa page, returning nil if there is no next page.
func scrapePagerNext(doc *goquery.Document, s *goquery.Selection) (*url.URL, error) {
	pager, err := findOne(s, `nav.pagerer-pager-basic[role="navigation"]`, "accessiblepager widget")
	if err != nil {
		return nil, err
	}

	next := pager.Find(`a[rel="next"]`)
	if n := next.Length(); n == 0 {
		if pager.Find(`a[rel="prev"]`).Length() == 0 {
			return nil, fmt.Errorf("no next or prev link found in pager")
		}
		return nil, nil
	} else if n > 1 {
		return nil, fmt.Errorf("multiple next links found (wtf)")
	}
	return resolve(doc, next)
}

// scrapePlaceListings iterates over the place listings table, returning the URL
// of the next page, if any.
func scrapePlaceListings(doc *goquery.Document, s *goquery.Selection, fn func(u *url.URL, name, address string) error) error {
	view, err := findOne(s, `.view-place-listing-search`, "place listing view")
	if err != nil {
		return err
	}

	table, err := findOne(view, `table`, "place listing result table")
	if err != nil {
		return err
	}

	rows := table.Find(`tbody > tr`)
	if rows.Length() == 0 {
		return fmt.Errorf("no rows found")
	}

	err = nil
	rows.EachWithBreak(func(i int, row *goquery.Selection) bool {
		if x := func() error {
			rowTitle, err := findOne(row, `td[headers="view-title-table-column"]`, "title column")
			if err != nil {
				return err
			}

			rowURL, err := findOne(rowTitle, `a[href]`, "row link")
			if err != nil {
				return err
			}

			rowAddress, err := findOne(row, `td[headers="view-field-address-table-column"]`, "address column")
			if err != nil {
				return err
			}

			u, err := resolve(doc, rowURL)
			if err != nil {
				return err
			}

			title := strings.TrimSpace(rowTitle.Text())
			address := strings.TrimSpace(rowAddress.Text())

			if err := fn(u, title, address); err != nil {
				return fmt.Errorf("process %q: %w", title, err)
			}
			return nil
		}(); x != nil {
			err = fmt.Errorf("row %d: %w", i+1, x)
		}
		return err == nil
	})
	return err
}
