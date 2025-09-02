//go:build ignore

package main

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/pgaskin/orec2/schema"
	"google.golang.org/protobuf/proto"
)

func main() {
	name := "data/data.pb"
	if len(os.Args) > 1 {
		name = os.Args[1]
	}
	var data schema.Data
	if buf, err := os.ReadFile(name); err != nil {
		panic(err)
	} else if err := proto.Unmarshal(buf, &data); err != nil {
		panic(err)
	}
	filter(&data, func(fac *schema.Facility, sg *schema.ScheduleGroup, sch *schema.Schedule, act *schema.Schedule_Activity, day *schema.Schedule_ActivityDay, tr *schema.TimeRange) bool {
		switch fac.GetName() {
		case "CARDELREC Recreation Complex Goulbourn",
			"Kanata Leisure Centre and Wave Pool",
			"Minto Recreation Complex - Barrhaven",
			"Richcraft Recreation Complex-Kanata",
			"Tony Graham Recreation Complex - Kanata":
			return false // exclude these facilities
		}

		if name := act.GetXName(); name != "" {
			switch {
			case strings.Contains(name, "adult skate"):
			case strings.Contains(name, "family skate"):
			case strings.Contains(name, "public skate"):
			//case strings.Contains(name, "lane swim"):
			default:
				return false // exclude other activities
			}
		}

		wd, cr, ok := tr2cr(tr)
		if !ok {
			return false // cannot filter
		}
		var want []schema.ClockRange
		switch wd {
		case time.Saturday, time.Sunday:
			want = append(want, schema.MakeClockRange(12, 00, 23, 00))
		default:
			want = append(want, schema.MakeClockRange(6, 00, 9, 00))
			want = append(want, schema.MakeClockRange(18, 00, 23, 00))
		}
		return slices.ContainsFunc(want, cr.Overlaps) // exclude if doesn't overlap a time we want
	})
	if err := tmpl.Execute(os.Stdout, &data); err != nil {
		panic(err)
	}
}

// tmpl is a template for rendering recreation schedules as text. It does not
// include information about schedule exceptions or scrape errors.
var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"ansi": ansi,
}).Parse(`
{{- range $f := .GetFacilities }}
{{- range $g := .GetScheduleGroups }}
{{- range $s := .GetSchedules }}
{{ansi 33}}{{.GetCaption}}{{ansi}}
{{- range .GetActivities }}
+ {{ansi 33}}{{.GetLabel}}{{ansi}}
    {{- range $i, $ts := .GetDays }}
    {{- if .GetTimes }}
    {{ansi 35}}{{index $s.GetDays $i}}{{ansi}}
    {{- end }}
    {{- range $j, $t := .GetTimes }}{{if gt (len $ts.GetTimes) 1}}
      {{else}} {{end}}{{.GetLabel}}
    {{- end }}
    {{- end }}
{{""}}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
`))

// ansi formats an ansi escape sequence.
func ansi(i ...int) string {
	if len(i) == 0 {
		return "\x1b[0m"
	}
	var b strings.Builder
	b.WriteString("\x1b[")
	for x, y := range i {
		if x != 0 {
			b.WriteByte(';')
		}
		b.WriteString(strconv.Itoa(y))
	}
	b.WriteByte('m')
	return b.String()
}

// filter filters activity times with the specified function, removing any empty
// activities, schedules, schedule groups, or facilities.
func filter(data *schema.Data, fn func(*schema.Facility, *schema.ScheduleGroup, *schema.Schedule, *schema.Schedule_Activity, *schema.Schedule_ActivityDay, *schema.TimeRange) bool) {
	data.SetFacilities(slices.DeleteFunc(data.GetFacilities(), func(facility *schema.Facility) bool {
		facility.SetScheduleGroups(slices.DeleteFunc(facility.GetScheduleGroups(), func(group *schema.ScheduleGroup) bool {
			group.SetSchedules(slices.DeleteFunc(group.GetSchedules(), func(schedule *schema.Schedule) bool {
				schedule.SetActivities(slices.DeleteFunc(schedule.GetActivities(), func(activity *schema.Schedule_Activity) bool {
					for _, day := range activity.GetDays() {
						day.SetTimes(slices.DeleteFunc(day.GetTimes(), func(time *schema.TimeRange) bool {
							return !fn(facility, group, schedule, activity, day, time)
						}))
					}
					return !slices.ContainsFunc(activity.GetDays(), func(day *schema.Schedule_ActivityDay) bool {
						return len(day.GetTimes()) != 0
					})
				}))
				return len(schedule.GetActivities()) == 0
			}))
			return len(group.GetSchedules()) == 0
		}))
		return len(facility.GetScheduleGroups()) == 0
	}))
}

// tr2cr gets the parsed time range, if parseable.
func tr2cr(tr *schema.TimeRange) (time.Weekday, schema.ClockRange, bool) {
	if !tr.HasXWkday() || !tr.HasXStart() || !tr.HasXEnd() {
		return 0, schema.ClockRange{}, false
	}
	return time.Weekday(tr.GetXWkday()), schema.ClockRange{Start: schema.ClockTime(tr.GetXStart()), End: schema.ClockTime(tr.GetXEnd())}, true
}
