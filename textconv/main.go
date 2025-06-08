// Command textconv formats orec data in a human-readable way suitable for use
// with "git diff". The output may not be stable across versions.
package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/mitchellh/go-wordwrap"
	"github.com/pgaskin/orec2/schema"
	"google.golang.org/protobuf/proto"
)

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"wrap": func(n uint, s string) string {
		return wordwrap.WrapString(s, n)
	},
	"prefix": func(p, s string) string {
		var b strings.Builder
		for l := range strings.Lines(s) {
			if strings.TrimSpace(l) != "" {
				b.WriteString(p)
			}
			b.WriteString(l)
		}
		return b.String()
	},
	"overwrite": func(p, s string) string {
		if len(s) < len(p) {
			return p
		}
		return p + s[len(p):]
	},
	"kv": func(k, v string) string {
		if v == "" {

		}
		return " " + k + "=" + strconv.Quote(v)
	},
	"strtrunc": func(n int, s string) string {
		if len(s) > n {
			return s[:n]
		}
		return s
	},
	"wkdayshort": func(x *schema.Weekday) string {
		if x == nil {
			return ""
		}
		switch x.AsWeekday() {
		case time.Sunday:
			return "su"
		case time.Monday:
			return "mo"
		case time.Tuesday:
			return "tu"
		case time.Wednesday:
			return "we"
		case time.Thursday:
			return "th"
		case time.Friday:
			return "fr"
		case time.Saturday:
			return "sa"
		default:
			panic("invalid weekday")
		}
	},
	"trim":  strings.TrimSpace,
	"quote": strconv.Quote,
	"upper": strings.ToUpper,
	"tr2cr": func(x *schema.TimeRange) *schema.ClockRange {
		if x.XStart == nil || x.XEnd == nil {
			return nil
		}
		return &schema.ClockRange{
			Start: schema.ClockTime(*x.XStart),
			End:   schema.ClockTime(*x.XEnd),
		}
	},
}).Parse(`
{{- range $i, $a := .Attribution }}{{if $i}}{{"\n"}}{{end}}{{$a}}{{ end -}}

{{- range $fi, $f := .Facilities }}
{{- "\n\n======\n\n" -}}

{{ $f.Name }}
{{"  "}}{{ with $f.Source }}{{.Url}}{{ end }}

{{- with $f.XErrors }}
{{"\n+ "}}ERRORS ({{$f.Name}})
{{- range . }}
- {{.}}
{{- end }}
{{- end }}

{{- with $f.Address }}
{{"\n+ "}}ADDRESS ({{$f.Name}})
{{ . | trim | wrap 120 | prefix "  " }}
{{ with $f.XLnglat }}{{"  "}}{{.Lng}}, {{.Lat}}{{ end }}
{{- end }}

{{- with $f.Description }}
{{"\n+ "}}DESCRIPTION ({{$f.Name}})
{{ . | trim | wrap 120 | prefix "  " }}
{{- end }}

{{- with $f.NotificationsHtml }}
{{"\n+ "}}NOTIFICATIONS ({{$f.Name}})
{{ . | trim | wrap 120 | prefix "  " }}
{{- end }}

{{- with $f.SpecialHoursHtml }}
{{"\n+ "}}SPECIAL HOURS ({{$f.Name}})
{{ . | trim | wrap 120 | prefix "  " }}
{{- end }}

{{- range $gi, $g := .ScheduleGroups }}
{{"\n+ "}}GROUP {{ or $g.XTitle $g.Label | quote }} ({{$f.Name}})

{{- with $g.ScheduleChangesHtml }}
{{"  + "}}SCHEDULE CHANGES
{{ . | trim | wrap 120 | prefix "  | " }}
{{- end }}

{{- range $si, $s := .Schedules }}
{{"  &  "}}{{$s.Caption}} ({{$f.Name}} > {{ or $g.XTitle $g.Label }})
{{- range $sai, $sa := .Activities }}
{{- range $di, $d := .Days }}
{{- range $dti, $dt := .Times }}
{{"    ~" -}}
{{" "}}[{{or (wkdayshort $dt.XWkday | upper) (index $s.Days $di) }} {{with (tr2cr $dt)}}{{.Format false}}{{else}}{{quote $dt.Label}}{{end -}}]{{/**/ -}}
{{" "}}{{or $sa.XName $sa.Label -}}
{{" "}}({{$f.Name}}){{/**/ -}}
{{- end }}
{{- end }}
{{- end }}
{{- end }}

{{- end }}

{{- end }}
`))

func main() {
	input := os.Stdin
	if len(os.Args) > 1 {
		f, err := os.Open(os.Args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		input = f
	}
	if len(os.Args) > 2 {
		fmt.Fprintf(os.Stderr, "error: too many arguments\n")
		os.Exit(1)
	}

	buf, err := io.ReadAll(input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var data schema.Data
	if err := proto.Unmarshal(buf, &data); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if err := tmpl.Execute(os.Stdout, &data); err != nil {
		fmt.Println()
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
