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
	"time"
	"unicode"

	"maps"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
	"golang.org/x/time/rate"
)

var (
	scrape       = flag.String("scrape", "", "parse data from pages into the specified file")
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

// note: underscored fields are ones which contain data parsed or otherwise
// enriched by the scraper rather than coming directly from the source page, and
// are set on a best-effort basis (if an error occurs, it is ignored)

type Data struct {
	Facilities  []Facility `json:"facilities"`
	Attribution []string   `json:"_attribution"`
}

type Facility struct {
	Name           string           `json:"name"`
	Description    string           `json:"desc"`
	Source         FacilitySource   `json:"src"`
	Location       FacilityLocation `json:"location"`
	Notifications  string           `json:"notifications_html"` // raw html
	SpecialHours   string           `json:"special_hours_html"` // raw html
	ScheduleGroups []ScheduleGroup  `json:"schedule_groups"`
	Errors         []string         `json:"_err"` // scrape errors
}

type FacilitySource struct {
	URL  string `json:"url"`
	Date *int64 `json:"_date"` // unix epoch seconds, null if unknown
}

type FacilityLocation struct {
	Address   string   `json:"addr"`
	Longitude *float64 `json:"_lng"` // null if unknown
	Latitude  *float64 `json:"_lat"` // null if unknown
}

type ScheduleGroup struct {
	Label           string     `json:"lbl"`
	Title           string     `json:"_title"`                // parsed out from the label and normalized
	ScheduleChanges string     `json:"schedule_changes_html"` // raw html
	Schedules       []Schedule `json:"schedules"`
}

type Schedule struct {
	Caption    string             `json:"cap"`
	Title      string             `json:"_title"` // parsed out from the caption and normalized (i.e., without facility name or date range)
	DayHeaders []string           `json:"day_headers"`
	Activities []ScheduleActivity `json:"activities"`
}

type ScheduleActivity struct {
	Label string        `json:"lbl"`
	Days  [][]TimeRange `json:"days"` // [len(DayHeaders)][]
}

type TimeRange struct {
	Label   string `json:"lbl"`
	Start   *int   `json:"_start"` // minutes from 00:00, -1 if none, null if parse error
	End     *int   `json:"_end"`   // minutes from 00:00, -1 if none, null if parse error
	Weekday *int   `json:"_wkday"` // sunday = 0, null if parse error
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
	var (
		data Data
		cur  = *placeListing
	)
	for cur != "" {
		doc, _, err := fetchPage(ctx, cur)
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
			var facility Facility
			facility.Name = strings.Map(func(r rune) rune {
				if unicode.Is(unicode.Pd, r) {
					return '-'
				}
				return r
			}, name)
			facility.Location.Address = address
			facility.Source.URL = u.String()
			facility.ScheduleGroups = []ScheduleGroup{}
			facility.Errors = []string{}

			if lng, lat, hasLngLat, err := geocode(ctx, address); err != nil {
				slog.Warn("failed to geocode place", "name", name, "address", address, "error", err)
				facility.Errors = append(facility.Errors, fmt.Sprintf("failed to resolve address: %v", err))
			} else if hasLngLat {
				facility.Location.Latitude = &lat
				facility.Location.Longitude = &lng
			}

			doc, date, err := fetchPage(ctx, u.String())
			if err != nil {
				slog.Warn("failed to fetch place", "name", name, "error", err)
				facility.Errors = append(facility.Errors, fmt.Sprintf("failed to fetch data: %v", err))
			} else {
				slog.Info("got place", "name", name)
			}
			if !date.IsZero() {
				ts := date.Unix()
				facility.Source.Date = &ts
			}
			if *scrape == "" {
				return nil
			}
			if err := func() error {
				content, err := scrapeMainContentBlock(doc)
				if err != nil {
					return err
				}

				node, err := findOne(content, `.node.node--type-place`, "place node")
				if err != nil {
					return err
				}

				if field, err := scrapeNodeField(node, "description", "text-long", false, true); err != nil {
					facility.Errors = append(facility.Errors, fmt.Sprintf("extract facility description: %v", err))
				} else {
					facility.Description = strings.Join(strings.Fields(field.Text()), " ")
				}

				if field, err := scrapeNodeField(node, "notification-details", "text-long", false, true); err != nil {
					facility.Errors = append(facility.Errors, fmt.Sprintf("extract facility notifications: %v", err))
				} else if raw, err := field.Html(); err != nil {
					facility.Errors = append(facility.Errors, fmt.Sprintf("extract facility notifications: %v", err))
				} else {
					facility.Notifications = raw
				}

				if field, err := scrapeNodeField(node, "hours-details", "text-long", false, true); err != nil {
					facility.Errors = append(facility.Errors, fmt.Sprintf("extract facility notifications: %v", err))
				} else if raw, err := field.Html(); err != nil {
					facility.Errors = append(facility.Errors, fmt.Sprintf("extract facility notifications: %v", err))
				} else {
					facility.SpecialHours = raw
				}

				if err := scrapeCollapseSections(node, func(label string, content *goquery.Selection) error {
					var group ScheduleGroup
					group.Label = label
					group.Schedules = []Schedule{}

					if !strings.Contains(label, "drop-in") && !strings.Contains(label, "schedule") && content.Find(`a[href*="reservation.frontdesksuite"],p:contains("schedules listed in the charts below"),th:contains("Monday")`).Length() == 0 {
						return nil // probably not a schedule group
					}

					title := strings.Map(func(r rune) rune {
						if unicode.Is(unicode.Pd, r) {
							return '-'
						}
						return unicode.ToLower(r)
					}, norm.NFKC.String(label))
					title = strings.TrimPrefix(title, "drop-in schedule")
					title = strings.TrimPrefix(title, "s ")
					title = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(title), "-"))
					title = cases.Title(language.English).String(title)
					group.Title = title

					if scheduleChangeH := content.Find("h1,h2,h3,h4,h5,h6").FilterFunction(func(i int, s *goquery.Selection) bool {
						return strings.HasPrefix(strings.TrimSpace(strings.ToLower(s.Text())), "schedule change")
					}); scheduleChangeH.Length() == 1 {
						if sel := scheduleChangeH.Next(); sel.Is("ul") {
							if raw, err := sel.Html(); err == nil {
								group.ScheduleChanges = "<ul>" + raw + "</ul>"
							} else {
								facility.Errors = append(facility.Errors, fmt.Sprintf("parse schedule changes for schedule group %q: %v", label, err))
							}
						} else {
							facility.Errors = append(facility.Errors, fmt.Sprintf("parse schedule changes for schedule group %q: header is not followed by a list", label))
						}
					} else if scheduleChangeH.Length() != 0 {
						facility.Errors = append(facility.Errors, fmt.Sprintf("parse schedule changes for schedule group %q: multiple selector matches found", label))
					}

					facility.ScheduleGroups = append(facility.ScheduleGroups, group)
					return nil
				}); err != nil {
					return err
				}

				return nil
			}(); err != nil {
				facility.Errors = append(facility.Errors, fmt.Sprintf("failed to extract facility information: %v", err))
			}

			data.Facilities = append(data.Facilities, facility)
			return nil
		}); err != nil {
			return err
		}

		if nextURL == nil {
			break
		}
		cur = nextURL.String()
	}
	if *scrape != "" {
		slog.Info("saving data to file", "name", *scrape)
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(data); err != nil {
			return fmt.Errorf("save data: %w", err)
		}
		if err := os.WriteFile(*scrape, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("save data: %w", err)
		}
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

func fetchPage(ctx context.Context, u string) (*goquery.Document, time.Time, error) {
	slog.Info("fetch page", "url", u)

	resp, err := fetch(ctx, u, "page", u, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, time.Time{}, err
	}
	doc.Url = resp.Request.URL

	date, _ := time.Parse(http.TimeFormat, resp.Header.Get("Date"))
	return doc, date, nil
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

// scrapeCollapseSections iterates over collapse section widgets contained
// within s.
func scrapeCollapseSections(s *goquery.Selection, fn func(title string, content *goquery.Selection) error) error {
	buttons := s.Find(`[role="button"][data-toggle="collapse"][data-target]`)
	if buttons.Length() == 0 && s.Find(`div.collapse-region`).Length() != 0 {
		return fmt.Errorf("no collapse sections found, but collapse-region found")
	}

	var err error
	buttons.EachWithBreak(func(i int, btn *goquery.Selection) bool {
		title := strings.TrimSpace(btn.Text())
		if x := func() error {
			tgt, _ := btn.Attr("data-target")

			content, err := findOne(s, tgt, "collapse section content")
			if err != nil {
				return err
			}

			if err := fn(title, content); err != nil {
				return fmt.Errorf("process %q: %w", title, err)
			}
			return nil
		}(); x != nil {
			err = fmt.Errorf("section %d (%q): %w", i+1, title, x)
		}
		return err == nil
	})
	return err
}

// scrapeNodeField gets a node field, ensuring it is the expected type.
func scrapeNodeField(s *goquery.Selection, name, typ string, array, optional bool) (*goquery.Selection, error) {
	fields := s.Find(".field")
	if fields.Length() == 0 {
		return nil, fmt.Errorf("no fields found")
	}

	fields = fields.Filter(".field--name-field-" + name)
	if fields.Length() == 0 {
		if optional {
			return fields, nil
		}
		return nil, fmt.Errorf("field %q not found", name)
	}

	if fields.Length() > 1 {
		return nil, fmt.Errorf("multiple (%d) fields with name %q found, expected one", fields.Length(), name)
	}
	field := fields.First()

	if !field.HasClass("field--type-" + typ) {
		return nil, fmt.Errorf("field %q does not have type %q", name, typ)
	}

	var (
		items   *goquery.Selection
		isArray bool
	)
	switch {
	case field.HasClass("field__items"):
		items = field.Find(".field__item")
		isArray = true
	case field.HasClass("field__item"):
		items = field
	default:
		if tmp := field.Find(".field__items"); tmp.Length() != 0 {
			items = tmp.Find(".field__item")
			isArray = true
		} else {
			items = field.Find(".field__item")
		}
	}
	if !isArray && items.Length() > 1 {
		return nil, fmt.Errorf("field %q is not an array, but found multiple field__item elements (wtf)", name)
	}
	if items.Length() == 0 {
		return nil, fmt.Errorf("field %q does not contain field__item value (wtf)", name)
	}
	if array != isArray {
		if array {
			return nil, fmt.Errorf("field %q is not an array, expected one", name)
		} else {
			return nil, fmt.Errorf("field %q is an array, expected not", name)
		}
	}
	return items, nil
}
