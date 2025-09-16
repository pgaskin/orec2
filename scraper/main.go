package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/PuerkitoBio/goquery"
	"github.com/pgaskin/orec2/schema"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	Scrape       = flag.String("scrape", "", "parse data from pages, writing the protobuf to the specified file")
	NoFetch      = flag.Bool("no-fetch", false, "don't fetch pages not in cache")
	CacheDir     = flag.String("cache-dir", "", "cache pages in the specified directory")
	Geocodio     = flag.Bool("geocodio", false, "use geocodio for geocoding (set GEOCODIO_APIKEY)")
	PageQPS      = flag.Float64("page-qps", 0.5, "maximum page fetches per second")
	PlaceListing = flag.String("place-listing", "https://ottawa.ca/en/recreation-and-parks/facilities/place-listing", "place listing url to start scraping from")
	UserAgent    = flag.String("user-agent", defaultUserAgent(), "user agent for requests")
	Zyte         = flag.Bool("zyte", false, "use zyte (set ZYTE_APIKEY)")
	ZyteLimit    = flag.Int("zyte-limit", 150, "zyte request limit")
)

var ScraperSecret = os.Getenv("OTTCA_SCRAPER_SECRET")

func defaultUserAgent() string {
	var ua strings.Builder
	ua.WriteString("ottawa-rec-scraper-bot/0.1")
	if ghRepo := os.Getenv("GITHUB_REPOSITORY"); ghRepo != "" {
		ghHost := cmp.Or(os.Getenv("GITHUB_SERVER_URL"), "https://github.com")
		if _, x, ok := strings.Cut(ghHost, "://"); ok {
			ghHost = x
		}
		ua.WriteString(" (")
		ua.WriteString(ghHost)
		ua.WriteString("/")
		ua.WriteString(ghRepo)
		ua.WriteString(")")
	} else {
		ua.WriteString(" (dev)")
	}
	return ua.String()
}

var geocodioLimit = sync.OnceValue(func() *rate.Limiter {
	return rate.NewLimiter(rate.Every(time.Minute/1000), 1)
})

var pageLimit = sync.OnceValue(func() *rate.Limiter {
	return rate.NewLimiter(rate.Limit(*PageQPS), 1)
})

func init() {
	if ScraperSecret != "" {
		sha := sha1.Sum([]byte(ScraperSecret))
		header := "X-Scraper-Secret"
		next := http.DefaultTransport
		http.DefaultTransport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix("."+strings.ToLower(r.URL.Hostname()), ".ottawa.ca") {
				r2 := *r
				r = &r2
				r.Header = maps.Clone(r.Header)
				r.Header[header] = []string{ScraperSecret}
			}
			resp, err := next.RoundTrip(r)
			if resp != nil && resp.Request != nil && resp.Request.Header != nil {
				if _, ok := resp.Request.Header[header]; ok {
					resp.Request.Header[header] = []string{"redacted-" + hex.EncodeToString(sha[:4])}
				}
			}
			return resp, err
		})
	}
	if apikey := os.Getenv("GEOCODIO_APIKEY"); apikey != "" {
		sha := sha1.Sum([]byte(apikey))
		header := "Authorization"
		next := http.DefaultTransport
		http.DefaultTransport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Hostname() == "api.geocod.io" {
				r2 := *r
				r = &r2
				r.Header = maps.Clone(r.Header)
				r.Header[header] = []string{"Bearer " + apikey}
			}
			resp, err := next.RoundTrip(r)
			if resp != nil && resp.Request != nil && resp.Request.Header != nil {
				if _, ok := resp.Request.Header[header]; ok {
					resp.Request.Header[header] = []string{"Bearer redacted-" + hex.EncodeToString(sha[:4])}
				}
			}
			return resp, err
		})
	}
	// TODO: refactor secret header/param redaction
	http.DefaultClient.Transport = http.DefaultTransport
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
	if *CacheDir != "" {
		slog.Info("using cache dir", "path", CacheDir)
		if err := os.Mkdir(*CacheDir, 0777); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create cache dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(*CacheDir, ".gitattributes"), []byte("* -text\n"), 0666); err != nil { // no line ending conversions
			return fmt.Errorf("write cache dir gitattributes: %w", err)
		}
	}
	if *NoFetch {
		slog.Info("only using cached data")
	} else {
		slog.Info("will fetch data", "ua", *UserAgent)
	}
	if *Scrape == "" {
		slog.Info("will not parse data")
	}
	if *Geocodio {
		slog.Info("using geocodio for geocoding")
	} else {
		slog.Warn("not geocoding addresses")
	}
	if *Zyte {
		slog.Info("using zyte", "limit", *ZyteLimit)
	}
	if !*Zyte && ScraperSecret != "" {
		slog.Info("using scraper secret")
	}
	var (
		data      schema.Data_builder
		geoAttrib = map[string]struct{}{}
		cur       = *PlaceListing
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
			var facility schema.Facility_builder
			facility.Name = name
			facility.Address = address
			facility.Source = schema.Source_builder{
				Url: u.String(),
			}.Build()

			if !*Geocodio {
				// skip geocoding
			} else if lng, lat, attrib, hasLngLat, err := geocode(ctx, address); err != nil {
				slog.Warn("failed to geocode place", "name", name, "address", address, "error", err)
				facility.XErrors = append(facility.XErrors, fmt.Sprintf("failed to resolve address: %v", err))
			} else if hasLngLat {
				facility.XLnglat = schema.LngLat_builder{
					Lat: float32(lat),
					Lng: float32(lng),
				}.Build()
				if attrib != "" {
					geoAttrib[attrib] = struct{}{}
				}
			}

			doc, date, err := fetchPage(ctx, u.String())
			if err != nil {
				slog.Warn("failed to fetch place", "name", name, "error", err)
				facility.XErrors = append(facility.XErrors, fmt.Sprintf("failed to fetch data: %v", err))
				data.Facilities = append(data.Facilities, facility.Build())
				return nil
			} else {
				slog.Info("got place", "name", name)
			}
			if !date.IsZero() {
				facility.Source.SetXDate(timestamppb.New(date))
			}
			if *Scrape == "" {
				return nil
			}
			if err := func() error {
				content, err := scrapeMainContentBlock(doc)
				if err != nil {
					if tmp, err := url.Parse(cur); err == nil && !strings.EqualFold(doc.Url.Hostname(), tmp.Hostname()) {
						return fmt.Errorf("facility page %q is not a City of Ottawa webpage", doc.Url)
					}
					return err
				}

				node, err := findOne(content, `.node.node--type-place`, "place node")
				if err != nil {
					return err
				}

				if field, err := scrapeNodeField(node, "description", "text-long", false, true); err != nil {
					facility.XErrors = append(facility.XErrors, fmt.Sprintf("extract facility description: %v", err))
				} else {
					facility.Description = strings.Join(strings.Fields(field.Text()), " ")
				}

				if field, err := scrapeNodeField(node, "notification-details", "text-long", false, true); err != nil {
					facility.XErrors = append(facility.XErrors, fmt.Sprintf("extract facility notifications: %v", err))
				} else if raw, err := field.Html(); err != nil {
					facility.XErrors = append(facility.XErrors, fmt.Sprintf("extract facility notifications: %v", err))
				} else {
					facility.NotificationsHtml = raw
				}

				if field, err := scrapeNodeField(node, "hours-details", "text-long", false, true); err != nil {
					facility.XErrors = append(facility.XErrors, fmt.Sprintf("extract facility notifications: %v", err))
				} else if raw, err := field.Html(); err != nil {
					facility.XErrors = append(facility.XErrors, fmt.Sprintf("extract facility notifications: %v", err))
				} else {
					facility.SpecialHoursHtml = raw
				}

				if err := scrapeCollapseSections(node, func(label string, content *goquery.Selection) error {
					if !strings.Contains(label, "drop-in") && !strings.Contains(label, "schedule") && content.Find(`a[href*="reservation.frontdesksuite"],p:contains("schedules listed in the charts below"),th:contains("Monday")`).Length() == 0 {
						return nil // probably not a schedule group
					}

					var group schema.ScheduleGroup_builder
					group.Label = label
					group.XTitle = extractScheduleGroupTitle(label)

					if scheduleChangeH := content.Find("h1,h2,h3,h4,h5,h6").FilterFunction(func(i int, s *goquery.Selection) bool {
						return strings.HasPrefix(strings.TrimSpace(strings.ToLower(s.Text())), "schedule change")
					}); scheduleChangeH.Length() == 1 {
						if sel := scheduleChangeH.Next(); sel.Is("ul") {
							if raw, err := sel.Html(); err == nil {
								group.ScheduleChangesHtml = "<ul>" + raw + "</ul>"
							} else {
								facility.XErrors = append(facility.XErrors, fmt.Sprintf("parse schedule changes for schedule group %q: %v", label, err))
							}
						} else {
							facility.XErrors = append(facility.XErrors, fmt.Sprintf("parse schedule changes for schedule group %q: header is not followed by a list", label))
						}
					} else if scheduleChangeH.Length() != 0 {
						facility.XErrors = append(facility.XErrors, fmt.Sprintf("parse schedule changes for schedule group %q: multiple selector matches found", label))
					}

				schedule:
					for _, table := range content.Find("table").EachIter() {
						var schedule schema.Schedule_builder
						schedule.Caption = normalizeText(table.Find("caption").First().Text(), false, false)

						// date range suffix
						name, date, ok := cutDateRange(schedule.Caption)
						if ok {
							schedule.XDate = date
							if r, ok := parseDateRange(date); ok {
								schedule.XFrom = ptrTo(int32(r.From))
								schedule.XTo = ptrTo(int32(r.To))
							} else {
								facility.XErrors = append(facility.XErrors, fmt.Sprintf("schedule %q: failed to parse date range %q", schedule.Caption, date))
							}
						}
						// " schedule" suffix
						name = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(name), " schedule"))
						// facility name prefix
						if x, ok := strings.CutPrefix(name, strings.ToLower(facility.Name)); ok {
							name = x
						} else if x, y, ok := strings.Cut(name, "-"); ok && strings.HasPrefix(strings.ToLower(facility.Name), x) {
							name = strings.TrimSpace(y) // e.g., "Jack Purcell Community Centre" with "Jack Purcell - swim and aquafit - January 6 to April 6"
							// note: we shouldn't try to parse the date range
							// (Month DD[, YYYY] to [Month ]DD[, YYYY] OR
							// until|starting Month DD[, YYYY]) since it's
							// manually written and the year isn't automatically
							// added when the year changes, so it's hard to know
							// if we parsed it correctly
						}
						name = strings.TrimLeft(name, " -")
						schedule.XName = strings.TrimLeft(name, " -")

						// TODO: refactor
						for _, row := range table.Find("tr").EachIter() {
							cells := row.Find("th,td")
							if schedule.Days == nil {
								for i, cell := range cells.EachIter() {
									if i != 0 {
										schedule.Days = append(schedule.Days, strings.Join(strings.Fields(cell.Text()), " "))
									}
								}
							} else {
								var activity schema.Schedule_Activity_builder
								if cells.Length() != len(schedule.Days)+1 {
									facility.XErrors = append(facility.XErrors, fmt.Sprintf("failed to parse schedule %q (group %q): row size mismatch", schedule.Caption, group.Label))
									continue schedule
								}
								for i, cell := range cells.EachIter() {
									if i == 0 {
										activity.Label = normalizeText(cell.Text(), false, false)
										activity.XName = cleanActivityName(cell.Text())
									} else {
										hdr := schedule.Days[i-1]
										wkday := time.Weekday(-1)
										for wd := range 7 {
											if strings.Contains(strings.ToLower(hdr), strings.ToLower(time.Weekday(wd).String())[:3]) {
												if wkday == -1 {
													wkday = time.Weekday(wd)
												} else {
													slog.Warn("multiple weekday matches for header, ignoring", "schedule", schedule.Caption, "header", hdr)
													wkday = -1 // multiple matches
													break
												}
											}
										}
										if wkday == -1 {
											facility.XErrors = append(facility.XErrors, fmt.Sprintf("warning: failed to parse weekday from header %q", hdr))
										}
										times := []*schema.TimeRange{}
										for t := range strings.FieldsFuncSeq(cell.Text(), func(r rune) bool {
											return r == ','
										}) {
											if strings.Map(func(r rune) rune {
												if unicode.IsSpace(r) {
													return -1
												}
												return r
											}, normalizeText(t, false, true)) == "n/a" {
												continue
											}
											var trange schema.TimeRange_builder
											trange.Label = strings.TrimSpace(normalizeText(t, false, false))
											if wkday != -1 {
												trange.XWkday = ptrTo(schema.Weekday(wkday))
											}
											if r, ok := parseClockRange(t); ok {
												trange.XStart = ptrTo(int32(r.Start))
												trange.XEnd = ptrTo(int32(r.End))
											} else {
												slog.Warn("failed to parse time range", "range", t)
												facility.XErrors = append(facility.XErrors, fmt.Sprintf("warning: failed to parse time range %q", t))
											}
											times = append(times, trange.Build())
										}
										activity.Days = append(activity.Days, schema.Schedule_ActivityDay_builder{
											Times: times,
										}.Build())
									}
								}
								schedule.Activities = append(schedule.Activities, activity.Build())
							}
						}
						if len(schedule.Days) == 0 || len(schedule.Activities) == 0 {
							facility.XErrors = append(facility.XErrors, fmt.Sprintf("failed to parse schedule %q (group %q): invalid table layout", schedule.Caption, group.Label))
							continue schedule
						}

						group.Schedules = append(group.Schedules, schedule.Build())
					}

					facility.ScheduleGroups = append(facility.ScheduleGroups, group.Build())
					return nil
				}); err != nil {
					return err
				}

				return nil
			}(); err != nil {
				facility.XErrors = append(facility.XErrors, fmt.Sprintf("failed to extract facility information: %v", err))
			}

			data.Facilities = append(data.Facilities, facility.Build())
			return nil
		}); err != nil {
			return err
		}

		if nextURL == nil {
			break
		}
		cur = nextURL.String()
	}
	if *Scrape != "" {
		data.Attribution = append(data.Attribution, "Compiled data © Patrick Gaskin. https://github.com/pgaskin/orec2")
		data.Attribution = append(data.Attribution, "Facility information and schedules © City of Ottawa. "+*PlaceListing)
		for _, attrib := range slices.Sorted(maps.Keys(geoAttrib)) {
			data.Attribution = append(data.Attribution, "Address data "+strings.TrimPrefix(attrib, "Data "))
		}
		slog.Info("saving protobuf data to file", "name", *Scrape)
		if buf, err := (proto.MarshalOptions{
			Deterministic: true,
		}).Marshal(data.Build()); err != nil {
			return fmt.Errorf("save data: %w", err)
		} else if err := os.WriteFile(*Scrape, buf, 0644); err != nil {
			return fmt.Errorf("save data: %w", err)
		}
	}
	return nil
}

// geocode geocodes an address using geocodio.
//
// As of 2025-09-16, geocodio works better than nominatim and
// pelias/geocode.earth:
//
//   - Nominatim has free public instances with no api key required.
//   - Pelias has a hosted instance at geocode.earth free for open-source projects.
//   - Geocodio has a free tier.
//   - Other geocoding services are expensive or do not allow storing and creating derivative works from the results.
//   - Geocodio specializes in Canada/US addresses and is the best at resolving addresses with incorrect street names or containing subdivision names.
//   - Nominatim is fine for well-formed addresses, but is overly strict and fails to geocode ones that Geocodio can.
//   - Pelias and Geocodio resolve all addresses successfully.
//   - Pelias is better than Geocodio at choosing a point near the entrance instead of somewhere on the property.
//   - For incorrect street names, Geocodio is better at resolving them based on the postal code, but Pelias just ignores the street and chooses somewhere seemingly random.
func geocode(ctx context.Context, addr string) (lng, lat float64, attrib string, ok bool, err error) {
	resp, err := fetchGeocodio(ctx, &url.URL{
		Path: "v1.9/geocode",
		RawQuery: url.Values{
			"q":       {addr},
			"country": {"CA"},
		}.Encode(),
	})
	if err != nil {
		return 0, 0, "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var obj struct {
			Error string
		}
		if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil || obj.Error == "" {
			return 0, 0, "", false, fmt.Errorf("response status %d", resp.StatusCode)
		}
		return 0, 0, "", false, fmt.Errorf("response status %d: geocodio error: %q", resp.StatusCode, obj.Error)
	}

	var obj struct {
		Results []struct {
			Location struct {
				Lat float64
				Lng float64
			}
			Source string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil {
		return 0, 0, "", false, fmt.Errorf("decode geocodio response: %w", err)
	}
	if len(obj.Results) != 0 {
		r := obj.Results[0]
		if r.Location.Lat == 0 || r.Location.Lng == 0 {
			return 0, 0, "", false, fmt.Errorf("decode geocodio response: missing lng/lat")
		}
		return r.Location.Lng, r.Location.Lat, "via geocodio (" + r.Source + ")", true, nil
	}
	return 0, 0, "", false, nil
}

func fetchPage(ctx context.Context, u string) (*goquery.Document, time.Time, error) {
	slog.Info("fetch page", "url", u)

	resp, err := fetch(ctx, u, "page", u, pageLimit(), true)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, time.Time{}, err
	}
	doc.Url = resp.Request.URL

	if doc.Find(`#main-content, #ottux-header, meta[name='dcterms.title'], meta[content*='drupal']`).Length() == 0 {
		if h, _ := doc.Html(); strings.Contains(h, "Pardon Our Interruption") || strings.Contains(h, "showBlockPage()") || strings.Contains(h, "Request unsuccessful. Incapsula incident ID: ") {
			return nil, time.Time{}, fmt.Errorf("imperva blocked request")
		}
		return nil, time.Time{}, fmt.Errorf("page content not found, might be imperva")
	}

	date, _ := time.Parse(http.TimeFormat, resp.Header.Get("Date"))
	return doc, date, nil
}

func fetchGeocodio(ctx context.Context, r *url.URL) (*http.Response, error) {
	slog.Info("fetch geocodio", "url", r.String())

	u, err := url.Parse("https://api.geocod.io/")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	u = u.ResolveReference(r)
	maps.Copy(q, r.Query()) // keep orig query params
	u.RawQuery = q.Encode()

	return fetch(ctx, u.String(), "geocodio", r.String(), geocodioLimit(), false)
}

func fetch(ctx context.Context, u string, cacheType, cacheKey string, limiter *rate.Limiter, proxy bool) (*http.Response, error) {
	var cacheName string
	if *CacheDir != "" {
		s := sha1.Sum([]byte(cacheKey))
		cacheName = filepath.Join(*CacheDir, cacheType+"-"+hex.EncodeToString(s[:]))
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
		} else if *NoFetch {
			return nil, fmt.Errorf("get %q: fetch disabled, response not in cache", u)
		}
	}
	if resp == nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}

		if limiter != nil {
			if err := limiter.Wait(ctx); err != nil {
				return nil, err
			}
		}

		if proxy && *Zyte {
			resp, err = zyte(req, true)
		} else {
			req.Header.Set("User-Agent", *UserAgent)
			resp, err = http.DefaultClient.Do(req)
		}
		if err != nil {
			return nil, err
		}

		if cacheName != "" {
			defer resp.Body.Close()

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

// zyte does a request through zyte.
func zyte(req *http.Request, followRedirect bool) (*http.Response, error) {
	ctx := req.Context()

	zreqObj := map[string]any{
		"httpResponseBody":    true,
		"httpResponseHeaders": true,
		"url":                 req.URL.String(),
		"followRedirect":      followRedirect,
	}
	if req.Method != http.MethodGet {
		zreqObj["httpRequestMethod"] = req.Method
	}
	if req.Body != nil {
		defer req.Body.Close()
		buf, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("prepare body: %w", err)
		}
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(buf)), nil
		}
		req.Body, _ = req.GetBody()
		zreqObj["httpRequestBody"] = buf // base64
	}
	if cookies := req.Cookies(); len(cookies) != 0 {
		return nil, fmt.Errorf("cookies not supported")
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
		return nil, fmt.Errorf("prepare request: %w", err)
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
	retryBanLimit := 3
	for {
		if *ZyteLimit == 0 {
			return nil, fmt.Errorf("zyte request limit reached")
		}

		zreq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.zyte.com/v1/extract", bytes.NewReader(zreqBuf))
		if err != nil {
			return nil, fmt.Errorf("prepare request: %w", err)
		}
		if apikey := os.Getenv("ZYTE_APIKEY"); apikey != "" {
			zreq.SetBasicAuth(apikey, "")
		} else {
			return nil, fmt.Errorf("no api key")
		}

		zresp, err := http.DefaultClient.Do(zreq)
		if err != nil {
			return nil, err
		}
		if zresp.StatusCode == http.StatusTooManyRequests {
			s := zresp.Header.Get("Retry-After")
			slog.Warn("zyte rate limit", "retry-after", s)
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
			return nil, fmt.Errorf("failed to parse rate-limit retry-after %q", s)
		}
		if (zresp.StatusCode == 500 || zresp.StatusCode == 520) && retryBanLimit > 0 {
			slog.Warn("zyte temporary error, retrying in a second")
			time.Sleep(time.Second)
			retryBanLimit--
			continue
		}
		if *ZyteLimit > 0 {
			*ZyteLimit--
		}
		if zresp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(zresp.Body).Decode(&zrespObj); err != nil {
				return nil, fmt.Errorf("parse response: %w", err)
			}
			if zrespObj.StatusCode == 0 || zrespObj.URL == "" || zrespObj.HTTPResponseBody == nil || zrespObj.HTTPResponseHeaders == nil {
				return nil, fmt.Errorf("parse response: missing fields")
			}
			break
		}
		var zerr struct {
			Type   string `json:"type"`
			Title  string `json:"title"`
			Status int    `json:"status"`
			Detail string `json:"detail"`
		}
		if zrespBuf, err := io.ReadAll(zresp.Body); err != nil {
			return nil, fmt.Errorf("read error %d response: %w", zresp.StatusCode, err)
		} else if err := json.Unmarshal(zrespBuf, &zerr); err != nil {
			if len(zrespBuf) > 1024 {
				zrespBuf = zrespBuf[:1024]
			}
			return nil, fmt.Errorf("parse error %d response %q: %w", zresp.StatusCode, string(zrespBuf), err)
		}
		return nil, fmt.Errorf("zyte error %d: %s: %s", zerr.Status, zerr.Title, zerr.Detail)
	}

	freq := req.Clone(ctx)
	if ru, err := url.Parse(zrespObj.URL); err != nil {
		return nil, fmt.Errorf("parse response url: %w", err)
	} else {
		freq.URL = ru
		freq.Host = ru.Host
	}
	freq.Header["xxx-use-proxy"] = []string{"zyte"}

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
		fresp.Header[h.Name] = append(fresp.Header[h.Name], h.Value)
	}
	if buf := *zrespObj.HTTPResponseBody; len(buf) != 0 {
		for k := range fresp.Header {
			if textproto.CanonicalMIMEHeaderKey(k) == "Content-Encoding" {
				fresp.ContentLength = -1
				fresp.Uncompressed = true
				delete(fresp.Header, k)
			}
		}
		if fresp.Uncompressed {
			for k := range fresp.Header {
				if textproto.CanonicalMIMEHeaderKey(k) == "Content-Length" {
					delete(fresp.Header, k)
				}
			}
		}
		fresp.Body = io.NopCloser(bytes.NewReader(buf))
	}
	return fresp, nil
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

			title := normalizeText(rowTitle.Text(), false, false)
			address := normalizeText(rowAddress.Text(), true, false)

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

// normalizeText performs various transformations on s:
//   - remove invisible characters
//   - collapse some kinds of consecutive whitespace (excluding newlines unless requested, but including nbsp)
//   - replace all kinds of dashes with "-"
//   - perform unicode NFKC normalization
//   - optionally lowercase the string
//   - remove leading and trailing whitespace
func normalizeText(s string, newlines, lower bool) string {
	// normalize the string
	s = norm.NFKC.String(s)

	// transform characters
	s = strings.Map(func(r rune) rune {

		// remove zero-width spaces
		switch r {
		case '\u200b', '\ufeff', '\u200d', '\u200c':
			return -1
		}

		// replace some whitespace for collapsing later
		switch r {
		case '\n':
			if newlines {
				return r
			}
			fallthrough
		case ' ', '\t', '\v', '\f', '\u00a0':
			return ' '
		}
		if unicode.Is(unicode.Zs, r) {
			return ' '
		}

		// replace smart punctuation
		switch r {
		case '“', '”', '‟':
			return '"'
		case '\u2018', '\u2019', '\u201b':
			return '\''
		case '\u2039':
			return '<'
		case '\u203a':
			return '>'
		}

		// normalize all kinds of dashes
		if unicode.Is(unicode.Pd, r) {
			return '-'
		}

		// remove invisible characters
		if !unicode.IsGraphic(r) {
			return -1
		}

		// lowercase (or not)
		if lower {
			return unicode.ToLower(r)
		}
		return r
	}, s)

	// collapse consecutive whitespace
	s = string(slices.CompactFunc([]rune(s), func(a, b rune) bool {
		return a == ' ' && a == b
	}))

	// remove leading/trailing whitespace
	return strings.TrimSpace(s)
}

// extractScheduleGroupTitle extracts the title of the schedule group from a
// section title.
func extractScheduleGroupTitle(s string) (title string) {
	title = normalizeText(s, false, true)
	title = strings.TrimPrefix(title, "drop-in schedule")
	title = strings.TrimPrefix(title, "s ")
	title = strings.Trim(title, "- ")
	title = cases.Title(language.English).String(title)
	return
}

// ageRangeRe matches things like "12+", "(18+)", and "(50 +)", also capturing
// the surrounding dashes/whitespace.
var ageRangeRe = regexp.MustCompile(`(^|[\s-]+)\(?(?:ages\s+)?([0-9]+)(?:\s*\+)\)?([\s(-]+|$)`) // capture: pre-sep age post-sep

// cutAgeMin removes the age minimum from activity, returning it as an int.
func cutAgeMin(activity string) (string, int, bool) {
	if ms := ageRangeRe.FindAllStringSubmatch(activity, -1); len(ms) == 1 {
		var (
			whole   = ms[0][0]
			preSep  = ms[0][1]
			ageStr  = ms[0][2]
			postSep = ms[0][3]
		)
		if age, err := strconv.ParseInt(ageStr, 0, 10); err == nil && age > 0 && age < 150 {
			sep := cmp.Or(preSep, postSep)
			if sep != "" && strings.TrimSpace(sep) == "" {
				if strings.TrimSpace(postSep) == "" {
					sep = " " // collapse if all whitespace
				} else {
					sep = postSep // pre is all whitespace, but post isn't
				}
			}
			return strings.TrimSpace(strings.ReplaceAll(activity, whole, sep)), int(age), true
		}
	}
	return activity, -1, false
}

// cutReservationRequirement removes the reservations (not) required text
// (prefixed by an asterisk) from activity.
func cutReservationRequirement(activity string) (string, bool, bool) {
	if i := strings.LastIndex(activity, "*"); i != -1 {
		switch normalizeText(strings.Trim(activity[i:], "*. ()"), false, true) {
		case "reservations not required", "reservation not required":
			return strings.TrimSpace(activity[:i]), false, true
		case "reservations required", "reservation required", "requires reservations", "requires reservation":
			return strings.TrimSpace(activity[:i]), true, true
		}
	}
	return activity, false, false
}

// reducedCapacityRe matches "reduced" or "reduced capacity" at the beginning or
// end of a string, optionally with spaces/dashes joining it to the rest of the
// string.
var reducedCapacityRe = regexp.MustCompile(`(?i)(?:^reduced(?:\s* capacity)?[\s-]*|[\s-]*reduced(?:\s* capacity)?$)`)

// cutReducedCapacity removes the reduced capacity text from activity. The
// activity name should have already been normalized and lowercased.
func cutReducedCapacity(activity string) (string, bool) {
	x := reducedCapacityRe.ReplaceAllLiteralString(activity, "")
	return x, x != activity
}

// activityReplacer normalizes word tenses and punctuation in activity names.
// The string should have already been normalized and lowercased.
var activityReplacer = strings.NewReplacer(
	"swimming", "swim",
	"aqualite", "aqua lite",
	"skating", "skate",
	"pick up ", "pick-up ",
	"pickup ", "pick-up ",
	"sport ", "sports ",
	" - courts", " court",
	" - court", " court",
	"®", "",
)

// cleanActivityName cleans up activity names.
func cleanActivityName(activity string) string {
	activity = normalizeText(activity, false, true)
	activity, _, _ = cutReservationRequirement(activity)
	activity, age, hasAge := cutAgeMin(activity)
	activity, reduced := cutReducedCapacity(activity)
	activity = activityReplacer.Replace(activity)
	if hasAge {
		activity = strings.TrimRight(activity, "- ") + " " + strconv.Itoa(age) + "+"
	}
	if reduced {
		activity += " - reduced capacity"
	}
	return normalizeText(activity, false, false)
}

// parseClockRange parses a time range for an activity.
func parseClockRange(s string) (r schema.ClockRange, ok bool) {
	strict := false

	s = strings.ReplaceAll(normalizeText(s, false, true), " ", "")

	parseSeparator := func(s string) (s1, s2 string, ok bool) {
		return stringsCutFirst(s, "-", "to")
	}

	parsePart := func(s string, mdef byte) (t schema.ClockTime, m byte, ok bool) {
		switch s {
		case "midnight":
			return schema.MakeClockTime(0, 0), 'a', true // midnight implies am
		case "noon":
			return schema.MakeClockTime(12, 0), 'p', true // noon implies pm
		}
		sh, sm, ok := strings.Cut(s, "h") // french time
		if !ok {
			if len(s) == 4 && strings.TrimFunc(s, func(r rune) bool { return r >= '0' && r <= '9' }) == "" {
				sh, sm, m = s[:2], s[2:], 0 // military time
			} else {
				if s, ok = strings.CutSuffix(s, "pm"); ok {
					if !strict {
						for {
							x, ok := strings.CutSuffix(strings.TrimRight(s, " "), "pm")
							if !ok {
								break
							}
							s = x // be lenient about duplicate pm suffixes
						}
					}
					m = 'p' // 12h pm
				} else if s, ok = strings.CutSuffix(s, "am"); ok {
					if !strict {
						for {
							x, ok := strings.CutSuffix(strings.TrimRight(s, " "), "am")
							if !ok {
								break
							}
							s = x // be lenient about duplicate am suffixes
						}
					}
					m = 'a' // 12h am
				} else {
					m = mdef // 24h or assumed am/pm
				}
				sh, sm, ok = strings.Cut(s, ":")
				if !ok {
					sm = "00" // no minute
				}
			}
		}
		if len(sh) > 2 || len(sm) > 2 {
			return 0, 0, false // invalid hour/minute length
		}
		hh, err := strconv.ParseInt(sh, 10, 0)
		if err != nil {
			return 0, 0, false // invalid hour
		}
		if m != 0 {
			if hh < 1 || hh > 12 {
				return 0, 0, false // invalid 12h hour
			}
			switch m {
			case 'p':
				if hh < 12 {
					hh += 12
				}
			case 'a':
				if hh == 12 {
					hh = 0
				}
			}
		} else {
			if hh < 0 || hh > 23 {
				return 0, 0, false // invalid 24h hour
			}
		}
		mm, err := strconv.ParseInt(sm, 10, 0)
		if err != nil {
			return 0, 0, false // invalid minute
		}
		if mm < 0 || mm > 59 {
			return 0, 0, false // invalid 24h minute
		}
		return schema.MakeClockTime(int(hh), int(mm)), m, true
	}

	if s == "" {
		return r, false // empty
	}
	s1, s2, ok := parseSeparator(s)
	if !ok {
		return r, false // single time
	}
	if !strict {
		for {
			s2a, s2b, ok := parseSeparator(s2)
			if !ok {
				break // no extraneous separators
			}
			if strings.TrimSpace(s2a) != "" || strings.TrimSpace(s2b) == "" {
				break // junk on the left side, or nothing on the right side
			}
			s2 = s2b // be lenient about extraneous separators with nothing in between (it's a frequent typo)
		}
	}
	if s1 == "" || s2 == "" {
		return r, false // open range
	}
	t1, m1, ok := parsePart(s1, 0)
	if !ok {
		return r, false // invalid rhs
	}
	t2, m2, ok := parsePart(s2, 0)
	if !ok {
		return r, false // invalid rhs
	}
	if m1 != 0 && m2 == 0 {
		return r, false // ambiguous lhs 12h and rhs 24h
	}
	if m1 == 0 && m2 != 0 {
		t1, m1, ok = parsePart(s1, m2) // reparse lhs with 12h rhs am/pm
		if !ok {
			return r, false // lhs hour is now invalid
		}
		if m1 != m2 {
			panic("wtf") // should be impossible (m1 == 0 thus it didn't include am/pm, and we reparsed it with m2)
		}
	}
	if t1 == t2 {
		return r, false // zero range
	}
	if t1 > t2 {
		t2 += 24 * 60 // next day
	}
	return schema.ClockRange{Start: t1, End: t2}, true
}

var cutDateRangeRe = sync.OnceValue[*regexp.Regexp](func() *regexp.Regexp {
	var b strings.Builder
	b.WriteString(`(?i)`)                 // case-insensitive
	b.WriteString(`^`)                    // anchor
	b.WriteString(`\s*`)                  // trim whitespace
	b.WriteString(`(.+?)`)                // prefix
	b.WriteString(`[ -]*[-][ -]*`)        // separator (spaces/dashes around at least one dash)
	b.WriteString(`((?:(?:[a-z]+|)\s*)?`) // date range modifier
	b.WriteString(`(?:`)                  // start of date range:
	b.WriteString(`(?:`)                  // ... month
	for i := range 12 {
		x := time.Month(1 + i).String()
		if i != 0 {
			b.WriteString(`|`)
		}
		b.WriteString(x[:3]) // first 3
		b.WriteString(`|`)
		b.WriteString(x) // or the whole thing
	}
	b.WriteString(`)(?:$|[ ,])`) // ... ... followed by a space or comma or end
	b.WriteString(`|(?:`)        // ... or weekday
	for i := range 7 {
		x := time.Weekday(i).String()
		if i != 0 {
			b.WriteString(`|`)
		}
		b.WriteString(x[:3]) // first 3
		b.WriteString(`|`)
		b.WriteString(x) // or the whole thing
	}
	b.WriteString(`)(?:$|[ ,])`) // ... ... followed by a space or comma or end
	b.WriteString(`).*)`)        // and the rest
	b.WriteString(`\s*`)         // trim whitespace
	b.WriteString(`$`)           // anchor
	return regexp.MustCompile(b.String())
})

// cutDateRange cuts s around the first match of spacs/dash characters followed
// by a month+space, day+space, or day+comma or day (3 letters) and a
// non-alphanumeric character. For best results, the string should have already
// been normalized.
//
// note: we do it this way so we can be sure we didn't leave part of a date
// behind with parseDateRange.
func cutDateRange(s string) (prefix, dates string, ok bool) {
	if m := cutDateRangeRe().FindStringSubmatch(s); m != nil {
		return m[1], m[2], true
	}
	return s, "", false
}

// parseDateRange parses a schedule date range. If successful, the range will
// always have at least the month and day set on one side.
func parseDateRange(s string) (r schema.DateRange, ok bool) {
	s = normalizeText(s, false, true)

	var starting, until bool
	if s, starting = strings.CutPrefix(s, "starting "); !starting {
		s, until = strings.CutPrefix(s, "until ")
	}

	var and, to bool
	leftStr, rightStr, to := strings.Cut(s, " to ")
	if !to {
		leftStr, rightStr, and = strings.Cut(s, " and ")
	}
	if (and || to) && (starting || until) {
		return r, false // can't both be a range and a one-sided date
	}

	parsePart := func(s string) (schema.Date, bool) {
		var (
			ok    bool
			rest               = s
			year  int          = 0
			month time.Month   = 0
			day   int          = 0
			wkday time.Weekday = -1
		)

		// parse a weekday or month
		rest = strings.TrimSpace(rest)
		s, rest, _ = stringsCutFirst(rest, ",", " ")
		s = strings.TrimSpace(s)
		ok = false
		for i := range 7 {
			v := time.Weekday(i)
			x := v.String()
			if strings.EqualFold(s, x) || strings.EqualFold(s, x[:3]) {
				wkday, ok = v, true
				break
			}
		}
		if !ok {
			for i := range 12 {
				v := time.Month(i + 1)
				x := v.String()
				if strings.EqualFold(s, x) || strings.EqualFold(s, x[:3]) {
					month, ok = v, true
					break
				}
			}
		}
		if !ok {
			return schema.MakeDate(year, month, day, wkday), false // no weekday/month found
		}

		// if wasn't a month, parse the month next
		if month == 0 {
			rest = strings.TrimSpace(rest)
			s, rest, _ = stringsCutFirst(rest, ",", " ")
			s = strings.TrimSpace(s)
			ok = false
			for i := range 12 {
				v := time.Month(i + 1)
				x := v.String()
				if strings.EqualFold(s, x) || strings.EqualFold(s, x[:3]) {
					month, ok = v, true
					break
				}
			}
			if !ok {
				return schema.MakeDate(year, month, day, wkday), false // no month found after weekday
			}
		}

		// parse the day
		rest = strings.TrimSpace(rest)
		s, rest, _ = stringsCutFirst(rest, ",", " ")
		s = strings.TrimSpace(s)
		ok = false
		if v, err := strconv.ParseInt(s, 10, 0); err == nil && v >= 1 && v <= 32 {
			day, ok = int(v), true
		}
		if !ok {
			return schema.MakeDate(year, month, day, wkday), false // invalid day
		}

		// if there's anything left, parse the year
		if rest != "" {
			rest = strings.TrimSpace(rest)
			s, rest, _ = stringsCutFirst(rest, ",", " ")
			s = strings.TrimSpace(s)
			ok = false
			if v, err := strconv.ParseInt(s, 10, 0); err == nil && v > 2000 && v < 4000 {
				year, ok = int(v), true
			}
			if !ok {
				return schema.MakeDate(year, month, day, wkday), false // invalid day
			}
		}

		// check for trailing junk
		if rest != "" {
			return schema.MakeDate(year, month, day, wkday), false // trailing junk
		}

		// make the date and check it
		d := schema.MakeDate(year, month, day, wkday)
		ok = d.IsValid()
		return d, ok
	}

	left, ok := parsePart(leftStr)
	if !ok {
		return r, false // failed to parse left side or single
	}

	switch {
	case to, and: // ... and/to ...
		var right schema.Date
		if and {
			if _, hasYear := left.Year(); hasYear {
				return r, false // cannot have year for an "and" range
			}
		}
		if day, err := strconv.ParseInt(rightStr, 10, 0); err == nil && day >= 1 && day <= 32 {
			year, hasYear := left.Year()
			if !hasYear {
				year = 0
			}
			month, hasMonth := left.Month()
			if !hasMonth {
				month = 0
			}
			if and {
				leftDay, hasLeftDay := left.Day()
				if !hasLeftDay {
					return r, false // must have left day for an "and" range
				}
				if leftDay+1 != int(day) {
					return r, false // right day must be 1 more than the left day for an "and" range
				}
			}
			right, ok = schema.MakeDate(year, month, int(day), -1), true
		} else if and {
			return r, false // must only have day number for an "and" range
		} else {
			right, ok = parsePart(rightStr)
		}
		if !ok {
			return r, false // failed to parse right side
		}
		r.From = left
		r.To = right

	case starting: // starting ...
		r.From = left

	case until: // until ...
		r.To = left

	default: // ...
		r.From = left
		r.To = left
	}
	return r, true
}

// stringsCutFirst is like [strings.Cut], but selects the earliest of multiple
// possible separators.
func stringsCutFirst(s string, sep ...string) (before, after string, ok bool) {
	sn, si := 0, -1
	for _, sep := range sep {
		if i := strings.Index(s, sep); i >= 0 {
			if si < 0 || i < si {
				sn, si = len(sep), i
			}
		}
	}
	if si >= 0 {
		return s[:si], s[si+sn:], true
	}
	return s, "", false
}

func ptrTo[T any](x T) *T {
	return &x
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

var _ http.RoundTripper = roundTripperFunc(nil)
