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

func main() {
	var data schema.Data
	if buf, err := os.ReadFile("data/data.pb"); err != nil {
		panic(err)
	} else if err := proto.Unmarshal(buf, &data); err != nil {
		panic(err)
	}
	data.SetFacilities(slices.DeleteFunc(data.GetFacilities(), func(facility *schema.Facility) bool {
		switch facility.GetName() {
		case "CARDELREC Recreation Complex Goulbourn",
			"Kanata Leisure Centre and Wave Pool",
			"Minto Recreation Complex - Barrhaven",
			"Richcraft Recreation Complex-Kanata",
			"Tony Graham Recreation Complex - Kanata":
			return true // exclude
		}
		facility.SetScheduleGroups(slices.DeleteFunc(facility.GetScheduleGroups(), func(group *schema.ScheduleGroup) bool {
			group.SetSchedules(slices.DeleteFunc(group.GetSchedules(), func(schedule *schema.Schedule) bool {
				schedule.SetActivities(slices.DeleteFunc(schedule.GetActivities(), func(activity *schema.Schedule_Activity) bool {
					switch {
					case strings.Contains(activity.GetXName(), "adult skate"):
					case strings.Contains(activity.GetXName(), "family skate"):
					case strings.Contains(activity.GetXName(), "public skate"):
					//case strings.Contains(activity.XName, "lane swim"):
					default:
						return true // include
					}
					for _, day := range activity.GetDays() {
						day.SetTimes(slices.DeleteFunc(day.GetTimes(), func(tr *schema.TimeRange) bool {
							if !tr.HasXWkday() || !tr.HasXStart() || !tr.HasXEnd() {
								return false // cannot filter
							}
							var tvals []schema.ClockRange
							switch time.Weekday(tr.GetXWkday()) {
							case time.Saturday, time.Sunday:
								tvals = append(tvals, schema.MakeClockRange(12, 00, 23, 00))
							default:
								tvals = append(tvals, schema.MakeClockRange(6, 00, 9, 00))
								tvals = append(tvals, schema.MakeClockRange(18, 00, 23, 00))
							}
							return !slices.ContainsFunc(tvals, func(v schema.ClockRange) bool {
								return schema.ClockRange{Start: schema.ClockTime(tr.GetXStart()), End: schema.ClockTime(tr.GetXEnd())}.Overlaps(v) // exclude if doesn't overlap
							})
						}))
					}
					return !slices.ContainsFunc(activity.GetDays(), func(day *schema.Schedule_ActivityDay) bool {
						return len(day.GetTimes()) != 0 // exclude if empty
					})
				}))
				return len(schedule.GetActivities()) == 0 // exclude if empty
			}))
			return len(group.GetSchedules()) == 0 // exclude if empty
		}))
		return len(facility.GetScheduleGroups()) == 0 // exclude if empty
	}))
	if err := tmpl.Execute(os.Stderr, &data); err != nil {
		panic(err)
	}
}
