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

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"ansi": func(i ...int) string {
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
	},
}).Parse(`
{{- range $f := .Facilities }}
{{- range $g := .ScheduleGroups }}
{{- range $s := .Schedules }}
{{ansi 33}}{{.Caption}}{{ansi}}
{{- range .Activities }}
+ {{ansi 33}}{{.Label}}{{ansi}}
    {{- range $i, $ts := .Days }}
    {{- if .Times }}
    {{ansi 35}}{{index $s.Days $i}}{{ansi}}
    {{- end }}
    {{- range $j, $t := .Times }}
      {{.Label}}
    {{- end }}
    {{- end }}
{{""}}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
`))

func main() {
	var data schema.Data
	if buf, err := os.ReadFile("data/data.pb"); err != nil {
		panic(err)
	} else if err := proto.Unmarshal(buf, &data); err != nil {
		panic(err)
	}
	data.Facilities = slices.DeleteFunc(data.Facilities, func(facility *schema.Facility) bool {
		switch facility.Name {
		case "CARDELREC Recreation Complex Goulbourn",
			"Kanata Leisure Centre and Wave Pool",
			"Minto Recreation Complex - Barrhaven",
			"Richcraft Recreation Complex-Kanata",
			"Tony Graham Recreation Complex - Kanata":
			return true // exclude
		}
		facility.ScheduleGroups = slices.DeleteFunc(facility.ScheduleGroups, func(group *schema.ScheduleGroup) bool {
			group.Schedules = slices.DeleteFunc(group.Schedules, func(schedule *schema.Schedule) bool {
				schedule.Activities = slices.DeleteFunc(schedule.Activities, func(activity *schema.Schedule_Activity) bool {
					switch {
					case strings.Contains(activity.XName, "adult skate"):
					case strings.Contains(activity.XName, "family skate"):
					case strings.Contains(activity.XName, "public skate"):
					//case strings.Contains(activity.XName, "lane swim"):
					default:
						return true // include
					}
					for _, day := range activity.Days {
						day.Times = slices.DeleteFunc(day.Times, func(tr *schema.TimeRange) bool {
							if tr.XWkday == nil || tr.XStart == nil || tr.XEnd == nil {
								return false // cannot filter
							}
							var tvals []schema.ClockRange
							switch time.Weekday(*tr.XWkday) {
							case time.Saturday, time.Sunday:
								tvals = append(tvals, schema.MakeClockRange(12, 00, 23, 00))
							default:
								tvals = append(tvals, schema.MakeClockRange(6, 00, 9, 00))
								tvals = append(tvals, schema.MakeClockRange(18, 00, 23, 00))
							}
							return !slices.ContainsFunc(tvals, func(v schema.ClockRange) bool {
								return schema.ClockRange{Start: schema.ClockTime(*tr.XStart), End: schema.ClockTime(*tr.XEnd)}.Overlaps(v) // exclude if doesn't overlap
							})
						})
					}
					return !slices.ContainsFunc(activity.Days, func(day *schema.Schedule_ActivityDay) bool {
						return len(day.Times) != 0 // exclude if empty
					})
				})
				return len(schedule.Activities) == 0 // exclude if empty
			})
			return len(group.Schedules) == 0 // exclude if empty
		})
		return len(facility.ScheduleGroups) == 0 // exclude if empty
	})
	if err := tmpl.Execute(os.Stderr, &data); err != nil {
		panic(err)
	}
}
