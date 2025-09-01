package main

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/pgaskin/orec2/schema"
	textpbfmt "github.com/protocolbuffers/txtpbfmt/parser"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	CSV    = flag.String("csv", "", "write csv to this directory")
	JSON   = flag.String("json", "", "write json to this file")
	PB     = flag.String("pb", "", "write binary protobuf to this file")
	TextPB = flag.String("textpb", "", "write textpb to this file")
	Sqlite = flag.String("sqlite", "", "write sqlite database to this file")
	Pretty = flag.Bool("pretty", false, "prettify output (-json -textpb)")
	// TODO: plain html schedule dump?
)

func init() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "%s [options] data.pb\n", os.Args[0])
		flag.PrintDefaults()
	}
}

func main() {
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(flag.Arg(0)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

const ddl = `
CREATE TABLE metadata (
	key TEXT,
	value TEXT
);

CREATE TABLE facilities (
	id INTEGER PRIMARY KEY,
	facility_url TEXT NOT NULL,
	facility_scraped_at DATETIME NOT NULL,
	facility_name TEXT NOT NULL,
	facility_description TEXT NOT NULL,
	facility_address TEXT NOT NULL,
	facility_longitude REAL, -- if resolved
	facility_latitude REAL, -- if resolved
	facility_notifications_html TEXT NOT NULL,
	facility_special_hours_html TEXT NOT NULL
);

CREATE TABLE scrape_errors (
	facility_id INTEGER REFERENCES facilities(id),
	message TEXT
);

CREATE TABLE schedule_groups (
	id INTEGER PRIMARY KEY,
	facility_id INTEGER REFERENCES facilities(id),
	schedule_group_name TEXT NOT NULL,
	schedule_group_name_raw TEXT NOT NULL,
	schedule_changes_html TEXT NOT NULL
);

CREATE TABLE schedules (
	id INTEGER PRIMARY KEY,
	schedule_group_id INTEGER REFERENCES schedule_groups(id),
	schedule_caption TEXT NOT NULL,
	schedule_caption_raw TEXT NOT NULL
);

CREATE TABLE days (
	id INTEGER PRIMARY KEY,
	day TEXT NOT NULL UNIQUE
);

CREATE TABLE activities (
	id INTEGER PRIMARY KEY,
	activity TEXT NOT NULL UNIQUE
);

CREATE TABLE schedule_times (
	schedule_id INTEGER REFERENCES schedules(id),
	day_id INTEGER REFERENCES days(id),
	activity_id INTEGER REFERENCES activities(id),
	raw_activity TEXT NOT NULL,
	raw_time TEXT NOT NULL,
	weekday TEXT, -- if parseable; lowercase first two chars of weekday name
	start INTEGER, -- if parseable; minutes since midnight 
	duration INTEGER -- if parseable; minutes
);

INSERT INTO days (day) VALUES -- for consistency
	('Sunday'),
	('Monday'),
	('Tuesday'),
	('Wednesday'),
	('Thursday'),
	('Friday'),
	('Saturday');

CREATE VIEW everything AS SELECT schedule_times.rowid AS id, *, (SELECT group_concat(message, x'0a') FROM scrape_errors WHERE facility_id = facilities.id) AS scrape_errors FROM schedule_times
	LEFT JOIN activities ON activity_id = activities.id
	LEFT JOIN days ON day_id = days.id
	LEFT JOIN schedules ON schedule_id = schedules.id
	LEFT JOIN schedule_groups ON schedule_group_id = schedule_groups.id
	LEFT JOIN facilities ON facility_id = facilities.id;

CREATE VIEW simplified AS SELECT facility_name, schedule_group_name, schedule_caption_raw, activity, weekday,
	CASE WHEN start IS NOT NULL AND duration IS NOT NULL THEN printf("%02d:%02d", start/60, start%60) ELSE raw_time END AS start_,
	CASE WHEN start IS NOT NULL AND duration IS NOT NULL THEN printf("%02d:%02d", (start+duration)%(24*60)/60, (start+duration)%(24*60)%60) ELSE NULL END AS end_,
	CASE WHEN start IS NOT NULL AND duration IS NOT NULL THEN printf("%d:%02d", duration/60, duration%60) ELSE NULL END AS duration_,
	start, CASE WHEN start IS NOT NULL AND duration IS NOT NULL THEN start+duration ELSE NULL END AS end, duration,
	schedule_changes_html, facility_special_hours_html, facility_scraped_at
FROM everything ORDER BY facility_name, activity, weekday, start;
`

func setupConn(c *sqlite3.Conn) error {
	if err := c.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		return err
	}
	return nil
}

func run(pb string) error {
	slog.Info("loading data", "pb", pb)
	var data schema.Data
	if buf, err := os.ReadFile(pb); err != nil {
		return fmt.Errorf("load data: %w", err)
	} else if err := proto.Unmarshal(buf, &data); err != nil {
		return fmt.Errorf("load data: %w", err)
	}

	if *PB != "" {
		slog.Info("writing pb", "name", *PB)
		if buf, err := (proto.MarshalOptions{
			Deterministic: true,
		}).Marshal(&data); err != nil {
			return fmt.Errorf("save data: %w", err)
		} else if err := os.WriteFile(*PB, buf, 0644); err != nil {
			return fmt.Errorf("save data: %w", err)
		}
	}

	if *TextPB != "" {
		slog.Info("writing textpb", "name", *TextPB, "pretty", *Pretty)
		opt := prototext.MarshalOptions{
			Multiline:    false,
			AllowPartial: false,
			EmitASCII:    !*Pretty,
		}
		buf, err := opt.Marshal(&data)
		if err != nil {
			return fmt.Errorf("export textpb: %w", err)
		}
		if *Pretty {
			buf, err = textpbfmt.FormatWithConfig(buf, textpbfmt.Config{
				ExpandAllChildren:        true,
				SkipAllColons:            true,
				AllowTripleQuotedStrings: true,
				WrapStringsAtColumn:      120,
				WrapHTMLStrings:          true,
				WrapStringsAfterNewlines: true,
			})
			if err != nil {
				return fmt.Errorf("format textpb: %w", err)
			}
		}
		if err := os.WriteFile(*TextPB, buf, 0644); err != nil {
			return fmt.Errorf("export textpb: %w", err)
		}
	}

	if *JSON != "" {
		slog.Info("writing json", "name", *JSON, "pretty", *Pretty)
		opt := protojson.MarshalOptions{
			EmitUnpopulated:   true,
			EmitDefaultValues: true,
			Multiline:         false,
			AllowPartial:      false,
			UseEnumNumbers:    true,
			UseProtoNames:     false,
		}
		buf, err := opt.Marshal(&data)
		if err != nil {
			return fmt.Errorf("export json: %w", err)
		}
		if *Pretty {
			var buf1 bytes.Buffer
			if err := json.Indent(&buf1, buf, "", "  "); err != nil {
				return fmt.Errorf("format json: %w", err)
			}
			buf = buf1.Bytes()
		}
		if err := os.WriteFile(*JSON, buf, 0644); err != nil {
			return fmt.Errorf("export json: %w", err)
		}
	}

	var db *sql.DB
	if *Sqlite != "" || *CSV != "" {
		slog.Info("creating sqlite database")

		if x, err := driver.Open(":memory:", setupConn); err != nil {
			return fmt.Errorf("initialize db: %w", err)
		} else {
			db = x
		}
		defer db.Close()

		if _, err := db.Exec(ddl); err != nil {
			return fmt.Errorf("initialize db: %w", err)
		}

		for _, attrib := range data.GetAttribution() {
			if _, err := db.Exec(`INSERT INTO metadata (key, value) VALUES ('attribution', ?)`, attrib); err != nil {
				return fmt.Errorf("insert attribution: %w", err)
			}
		}
		for _, facility := range data.GetFacilities() {
			var facilityID int64
			if err := db.QueryRow(
				`INSERT INTO facilities (
				facility_url, facility_scraped_at,
				facility_name, facility_description,
				facility_address, facility_longitude, facility_latitude,
				facility_notifications_html, facility_special_hours_html
			) VALUES (
				?, ?,
				?, ?,
				?, ?, ?,
				?, ?
			) RETURNING id`,
				facility.GetSource().GetUrl(), protoTimestampAsTimeOrZero(facility.GetSource().GetXDate()),
				facility.GetName(), facility.GetDescription(),
				facility.GetAddress(), lngOrNil(facility.GetXLnglat()), latOrNil(facility.GetXLnglat()),
				facility.GetNotificationsHtml(), facility.GetSpecialHoursHtml(),
			).Scan(&facilityID); err != nil {
				return fmt.Errorf("insert facility: %w", err)
			}
			for _, scrapeError := range facility.GetXErrors() {
				if _, err := db.Exec(`INSERT INTO scrape_errors (facility_id, message) VALUES (?, ?)`, facilityID, scrapeError); err != nil {
					return fmt.Errorf("insert scrape error: %w", err)
				}
			}
			for _, scheduleGroup := range facility.GetScheduleGroups() {
				var scheduleGroupID int64
				if err := db.QueryRow(
					`INSERT INTO schedule_groups (
					facility_id,
					schedule_group_name, schedule_group_name_raw,
					schedule_changes_html
				) VALUES (
					?,
					?, ?,
					?
				) RETURNING id`,
					facilityID,
					scheduleGroup.GetXTitle(), scheduleGroup.GetLabel(),
					scheduleGroup.GetScheduleChangesHtml(),
				).Scan(&scheduleGroupID); err != nil {
					return fmt.Errorf("insert schedule group: %w", err)
				}
				for _, schedule := range scheduleGroup.GetSchedules() {
					var scheduleID int64
					if err := db.QueryRow(
						`INSERT INTO schedules (
						schedule_group_id,
						schedule_caption, schedule_caption_raw
					) VALUES (
						?,
						?, ?
					) RETURNING id`,
						scheduleGroupID,
						schedule.GetCaption(), schedule.GetCaption(), // same for now, have both for compatibility if we decide to parse it more
					).Scan(&scheduleID); err != nil {
						return fmt.Errorf("insert schedule: %w", err)
					}
					dayIDs := make([]int64, len(schedule.GetDays()))
					for i, day := range schedule.GetDays() {
						dayID := &dayIDs[i]
						if err := db.QueryRow(`SELECT id FROM days WHERE day = ?`, day).Scan(dayID); err != nil {
							if errors.Is(err, sql.ErrNoRows) {
								err = db.QueryRow(`INSERT INTO days (day) VALUES (?) RETURNING id`, day).Scan(dayID)
							}
							if err != nil {
								return fmt.Errorf("insert day: %w", err)
							}
						}
					}
					for _, activity := range schedule.GetActivities() {
						var activityID int64
						if err := db.QueryRow(`SELECT id FROM activities WHERE activity = ?`, activity.GetXName()).Scan(&activityID); err != nil {
							if errors.Is(err, sql.ErrNoRows) {
								err = db.QueryRow(`INSERT INTO activities (activity) VALUES (?) RETURNING id`, activity.GetXName()).Scan(&activityID)
							}
							if err != nil {
								return fmt.Errorf("insert activity: %w", err)
							}
						}
						for activityDayIdx, activityDay := range activity.GetDays() {
							dayID := dayIDs[activityDayIdx]
							for _, activityTime := range activityDay.GetTimes() {
								if _, err := db.Exec(
									`INSERT INTO schedule_times (
									schedule_id, day_id, activity_id,
									raw_activity, raw_time,
									weekday, start, duration
								) VALUES (
									?, ?, ?,
									?, ?,
									?, ?, ?
								)`,
									scheduleID, dayID, activityID,
									activity.GetLabel(), activityTime.GetLabel(),
									wkday2OrNil(activityTime.GetXWkday(), activityTime.HasXWkday()), trangeStartOrNil(activityTime), trangeDurationOrNil(activityTime),
								); err != nil {
									return fmt.Errorf("insert activity time: %w", err)
								}
							}
						}
					}
				}
			}
		}
	}

	if *Sqlite != "" {
		slog.Info("writing sqlite db", "name", *Sqlite)
		if err := os.Remove(*Sqlite); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("export sqlite3: %w", err)
		}
		if _, err := db.Exec(`VACUUM INTO ` + sqlite3.Quote(*Sqlite)); err != nil {
			return fmt.Errorf("export sqlite3: %w", err)
		}
	}

	if *CSV != "" {
		slog.Info("writing csv", "dir", *CSV)
		if err := os.Mkdir(*CSV, 0777); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("export csv: %w", err)
		}
		tables, err := getSqliteTables(db)
		if err != nil {
			return fmt.Errorf("export csv: get tables: %w", err)
		}
		for _, table := range tables {
			if err := exportCSV(db, table, filepath.Join(*CSV, table+".csv")); err != nil {
				return fmt.Errorf("export csv: table %s: %w", table, err)
			}
		}
	}

	slog.Info("done")
	return nil
}

var weekdays = [7]string{
	time.Sunday:    "su",
	time.Monday:    "mo",
	time.Tuesday:   "tu",
	time.Wednesday: "we",
	time.Thursday:  "th",
	time.Friday:    "fr",
	time.Saturday:  "sa",
}

func trangeStartOrNil(x *schema.TimeRange) *int {
	if x != nil && x.HasXStart() && x.HasXEnd() && x.GetXEnd() >= x.GetXStart() {
		return pointer(int(x.GetXStart()))
	}
	return nil
}

func trangeDurationOrNil(x *schema.TimeRange) *int {
	if x != nil && x.HasXStart() && x.HasXEnd() && x.GetXEnd() >= x.GetXStart() {
		return pointer(int(x.GetXEnd() - x.GetXStart()))
	}
	return nil
}

func wkday2OrNil(x schema.Weekday, has bool) *string {
	if has {
		return pointer(weekdays[x.AsWeekday()])
	}
	return nil
}

func lngOrNil(lnglat *schema.LngLat) *float32 {
	if lnglat != nil {
		return pointer(lnglat.GetLng())
	}
	return nil
}

func latOrNil(lnglat *schema.LngLat) *float32 {
	if lnglat != nil {
		return pointer(lnglat.GetLat())
	}
	return nil
}

func pointer[T any](x T) *T {
	return &x
}

func protoTimestampAsTimeOrZero(t *timestamppb.Timestamp) time.Time {
	if t != nil {
		return t.AsTime()
	}
	return time.Time{}
}

func getSqliteTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func exportCSV(db *sql.DB, table, outname string) error {
	rows, err := db.Query(`SELECT * FROM ` + table)
	if err != nil {
		return err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}

	f, err := os.OpenFile(outname, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0666)
	if err != nil {
		return err
	}
	defer f.Close()

	cw := csv.NewWriter(f)

	cw.Write(cols)

	var (
		values    = make([]sql.NullString, len(cols))
		valueOuts = make([]any, len(cols))
		valueStrs = make([]string, len(cols))
	)
	for i := range values {
		valueOuts[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(valueOuts...); err != nil {
			return err
		}
		for i, v := range values {
			if v.Valid {
				valueStrs[i] = v.String
			} else {
				valueStrs[i] = ""
			}
		}
		cw.Write(valueStrs)
	}
	cw.Flush()

	if err := rows.Err(); err != nil {
		return err
	}
	if err := cw.Error(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return nil
}
