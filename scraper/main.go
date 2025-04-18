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
	"io/fs"
	"log/slog"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
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
	Nominatim    = flag.String("nominatim", "https://nominatim.geocoding.ai", "nominatim base url")
	NominatimQPS = flag.Float64("nominatim-qps", 1, "maximum nominatim queries per second")
	PlaceListing = flag.String("place-listing", "https://ottawa.ca/en/recreation-and-parks/facilities/place-listing", "place listing url to start scraping from")
	UserAgent    = flag.String("user-agent", defaultUserAgent(), "user agent for requests")
)

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

var nominatimLimit = sync.OnceValue(func() *rate.Limiter {
	return rate.NewLimiter(rate.Limit(*NominatimQPS), 1)
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
	var (
		data      schema.Data
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
			var facility schema.Facility
			facility.Name = strings.Map(func(r rune) rune {
				if unicode.Is(unicode.Pd, r) {
					return '-'
				}
				return r
			}, name)
			facility.Address = address
			facility.Source = &schema.Source{
				Url: u.String(),
			}

			if lng, lat, attrib, hasLngLat, err := geocode(ctx, address); err != nil {
				slog.Warn("failed to geocode place", "name", name, "address", address, "error", err)
				facility.XErrors = append(facility.XErrors, fmt.Sprintf("failed to resolve address: %v", err))
			} else if hasLngLat {
				facility.XLnglat = &schema.LngLat{
					Lat: float32(lat),
					Lng: float32(lng),
				}
				if attrib != "" {
					geoAttrib[attrib] = struct{}{}
				}
			}

			doc, date, err := fetchPage(ctx, u.String())
			if err != nil {
				slog.Warn("failed to fetch place", "name", name, "error", err)
				facility.XErrors = append(facility.XErrors, fmt.Sprintf("failed to fetch data: %v", err))
			} else {
				slog.Info("got place", "name", name)
			}
			if !date.IsZero() {
				facility.Source.XDate = timestamppb.New(date)
			}
			if *Scrape == "" {
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
					var group schema.ScheduleGroup
					group.Label = label

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
					group.XTitle = title

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
						var schedule schema.Schedule

						schedule.Caption = strings.Join(strings.Fields(table.Find("caption").First().Text()), " ")

						name := strings.Map(func(r rune) rune {
							if unicode.Is(unicode.Pd, r) {
								return '-'
							}
							return unicode.ToLower(r)
						}, norm.NFKC.String(schedule.Caption))
						if x, ok := strings.CutPrefix(name, strings.ToLower(facility.Name)); ok {
							name = x
						} else if x, y, ok := strings.Cut(name, "-"); ok && strings.HasPrefix(strings.ToLower(facility.Name), x) {
							name = strings.TrimSpace(y) // e.g., "Jack Purcell Community Centre" with "Jack Purcell - swim and aquafit - January 6 to April 6"

						}
						name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "-"))
						for m := range 12 {
							if x, _, ok := strings.Cut(name, "- "+strings.ToLower(time.Month(m + 1).String())[:3]); ok {
								name = x
								break
							}
						}
						name = strings.TrimSpace(strings.TrimSuffix(name, " schedule"))
						schedule.XName = name

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
								var activity schema.Schedule_Activity
								if cells.Length() != len(schedule.Days)+1 {
									facility.XErrors = append(facility.XErrors, fmt.Sprintf("failed to parse schedule %q (group %q): row size mismatch", schedule.Caption, group.Label))
									continue schedule
								}
								for i, cell := range cells.EachIter() {
									if i == 0 {
										activity.Label = strings.Join(strings.Fields(cell.Text()), " ")
										name := strings.Map(func(r rune) rune {
											if unicode.Is(unicode.Pd, r) {
												return '-'
											}
											return unicode.ToLower(r)
										}, norm.NFKC.String(activity.Label))
										name = strings.Join(strings.Fields(name), " ")
										name = strings.ReplaceAll(name, "swimming", "swim")
										name = strings.ReplaceAll(name, "aqualite", "aqua lite")
										name = strings.ReplaceAll(name, "skating", "skate")
										name = strings.ReplaceAll(name, "pick up", "pick-up")
										name = strings.ReplaceAll(name, "sport ", "sports ")
										name = strings.ReplaceAll(name, "®", "")
										name = strings.ReplaceAll(name, "50 +", "50+")
										name = strings.ReplaceAll(name, "18 +", "18+")
										name = strings.ReplaceAll(name, "16 +", "16+")
										name = strings.ReplaceAll(name, "- 50+", "50+")
										name = strings.ReplaceAll(name, "- 18+", "18+")
										name = strings.ReplaceAll(name, "- 16+", "16+")
										name, _, _ = strings.Cut(name, " *")
										name, reduced := strings.CutPrefix(name, "reduced ")
										if !reduced {
											name, reduced = strings.CutSuffix(name, "- reduced")
										}
										if reduced {
											name += " - reduced capacity"
										}
										activity.XName = name
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
											tnorm := strings.Map(func(r rune) rune {
												if unicode.IsSpace(r) || r == '\u200b' {
													return -1
												}
												if unicode.Is(unicode.Pd, r) {
													return '-'
												}
												return unicode.ToLower(r)
											}, t)
											if tnorm == "n/a" {
												continue
											}
											var trange schema.TimeRange
											trange.Label = strings.TrimSpace(t)
											if wkday != -1 {
												trange.XWkday = ptrTo(schema.Weekday(wkday))
											}
											if t1, t2, ok := strings.Cut(tnorm, "-"); ok && !strings.Contains(t2, "-") {
												var (
													tryParseTime12h = func(s string) (hh, mm int, hasAMPM, isPM, ok bool) { // note: hh and mm are always returned in 24h time
														s = strings.Map(func(r rune) rune {
															if unicode.IsSpace(r) {
																return -1
															}
															return unicode.ToLower(r)
														}, s)
														switch s {
														case "midnight":
															hh, mm = 0, 0
															hasAMPM, isPM = true, false
															ok = true
														case "noon":
															hh, mm = 12, 0
															hasAMPM, isPM = true, true
															ok = true
														default:
															s, isPM = strings.CutSuffix(s, "pm")
															if !isPM {
																s, hasAMPM = strings.CutSuffix(s, "am")
															} else {
																hasAMPM = true
															}
															shh, smm, hasMM := strings.Cut(s, ":")
															if !hasMM {
																smm = "00"
															}
															if nh, err := strconv.ParseInt(shh, 10, 0); err == nil && nh > 0 && nh <= 12 {
																if hasAMPM {
																	if isPM {
																		nh += 12
																	} else if nh == 12 {
																		nh = 0
																	}
																}
																if nm, err := strconv.ParseInt(smm, 10, 0); err == nil && nm >= 0 && nm < 60 {
																	hh = int(nh)
																	mm = int(nm)
																	ok = true
																}
															}
														}
														return
													}
													h1, m1, ap1, pm1, ok1 = tryParseTime12h(t1)
													h2, m2, ap2, pm2, ok2 = tryParseTime12h(t2)
												)
												if ok1 && ok2 && pm1 && !pm2 {
													ok1, ok2 = false, false // invalid time range with am/pm at start and none at end
												} // TODO: rewrite this stuff
												if ok1 && ok2 {
													if ap2 && !ap1 && pm2 {
														h1 += 12 // time range with pm at end and no am/pm at start
													}
													n1 := h1*60 + m1
													n2 := h2*60 + m2
													if n2 < n1 {
														n1 += 24 * 60 // next day
													}
													trange.XStart = ptrTo(int32(n1))
													trange.XEnd = ptrTo(int32(n2))
												}
												if !ok1 || !ok2 {
													slog.Warn("failed to parse time range", "range", t)
													facility.XErrors = append(facility.XErrors, fmt.Sprintf("warning: failed to parse time range %q", t))
												}
											}
											times = append(times, &trange)
										}
										activity.Days = append(activity.Days, &schema.Schedule_ActivityDay{
											Times: times,
										})
									}
								}
								schedule.Activities = append(schedule.Activities, &activity)
							}
						}
						if len(schedule.Days) == 0 || len(schedule.Activities) == 0 {
							facility.XErrors = append(facility.XErrors, fmt.Sprintf("failed to parse schedule %q (group %q): invalid table layout", schedule.Caption, group.Label))
							continue schedule
						}

						group.Schedules = append(group.Schedules, &schedule)
					}

					facility.ScheduleGroups = append(facility.ScheduleGroups, &group)
					return nil
				}); err != nil {
					return err
				}

				return nil
			}(); err != nil {
				facility.XErrors = append(facility.XErrors, fmt.Sprintf("failed to extract facility information: %v", err))
			}

			data.Facilities = append(data.Facilities, &facility)
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
		if buf, err := proto.Marshal(&data); err != nil {
			return fmt.Errorf("save data: %w", err)
		} else if err := os.WriteFile(*Scrape, buf, 0644); err != nil {
			return fmt.Errorf("save data: %w", err)
		}
	}
	return nil
}

func geocode(ctx context.Context, addr string) (lng, lat float64, attrib string, ok bool, err error) {
	resp, err := fetchNominatim(ctx, &url.URL{
		Path: "search",
		RawQuery: url.Values{
			"format": {"geocodejson"},
			"q":      {addr},
		}.Encode(),
	})
	if err != nil {
		return 0, 0, "", false, err
	}
	defer resp.Body.Close()

	var obj struct {
		Type      string
		Geocoding struct {
			Attribution string
		}
		Features []struct {
			Type     string
			Geometry struct {
				Type        string
				Coordinates []float64
			}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&obj); err != nil {
		return 0, 0, "", false, fmt.Errorf("decode geocodejson: %w", err)
	}
	if obj.Type != "FeatureCollection" {
		return 0, 0, "", false, fmt.Errorf("decode geocodejson: wrong type %q", obj.Type)
	}
	for _, f := range obj.Features {
		if f.Type == "Feature" {
			if f.Geometry.Type != "Point" {
				return 0, 0, "", false, fmt.Errorf("decode geocodejson: wrong feature geometry type %q", f.Geometry.Type)
			}
			if len(f.Geometry.Coordinates) != 2 {
				return 0, 0, "", false, fmt.Errorf("decode geocodejson: wrong feature geometry coordinates length %d", len(f.Geometry.Coordinates))
			}
			return f.Geometry.Coordinates[0], f.Geometry.Coordinates[1], obj.Geocoding.Attribution, true, nil
		}
	}
	return 0, 0, "", false, nil
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

	if doc.Find(`#main-content, #ottux-header, meta[name='dcterms.title'], meta[content*='drupal']`).Length() == 0 {
		if h, _ := doc.Html(); strings.Contains(h, "Pardon Our Interruption") || strings.Contains(h, "showBlockPage()") {
			return nil, time.Time{}, fmt.Errorf("imperva blocked request")
		}
		return nil, time.Time{}, fmt.Errorf("page content not found, might be imperva")
	}

	date, _ := time.Parse(http.TimeFormat, resp.Header.Get("Date"))
	return doc, date, nil
}

func fetchNominatim(ctx context.Context, r *url.URL) (*http.Response, error) {
	slog.Info("fetch nominatim", "url", r.String())

	u, err := url.Parse(*Nominatim)
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

		req.Header.Set("User-Agent", *UserAgent)

		if limiter != nil {
			if err := limiter.Wait(ctx); err != nil {
				return nil, err
			}
		}

		resp, err = http.DefaultClient.Do(req)
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

func ptrTo[T any](x T) *T {
	return &x
}
