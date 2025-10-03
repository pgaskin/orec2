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
	"github.com/pgaskin/ottrec/schema"
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
	"wkdayshort": func(x schema.Weekday) string {
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
		if !x.HasXStart() || !x.HasXEnd() {
			return nil
		}
		return &schema.ClockRange{
			Start: schema.ClockTime(x.GetXStart()),
			End:   schema.ClockTime(x.GetXEnd()),
		}
	},
}).Parse(`
{{- range $i, $a := .GetAttribution }}{{if $i}}{{"\n"}}{{end}}{{$a}}{{ end -}}

{{- range $fi, $f := .GetFacilities }}
{{- "\n\n======\n\n" -}}

{{ $f.GetName }}
{{"  "}}{{ with $f.GetSource }}{{.GetUrl}}{{ end }}

{{- with $f.GetXErrors }}
{{"\n+ "}}ERRORS ({{$f.GetName}})
{{- range . }}
- {{.}}
{{- end }}
{{- end }}

{{- with $f.GetAddress }}
{{"\n+ "}}ADDRESS ({{$f.GetName}})
{{ . | trim | wrap 120 | prefix "  " }}
{{ with $f.GetXLnglat }}{{"  "}}{{.GetLng}}, {{.GetLat}}{{ end }}
{{- end }}

{{- with $f.GetDescription }}
{{"\n+ "}}DESCRIPTION ({{$f.GetName}})
{{ . | trim | wrap 120 | prefix "  " }}
{{- end }}

{{- with $f.GetNotificationsHtml }}
{{"\n+ "}}NOTIFICATIONS ({{$f.GetName}})
{{ . | trim | wrap 120 | prefix "  " }}
{{- end }}

{{- with $f.GetSpecialHoursHtml }}
{{"\n+ "}}SPECIAL HOURS ({{$f.GetName}})
{{ . | trim | wrap 120 | prefix "  " }}
{{- end }}

{{- range $gi, $g := .GetScheduleGroups }}
{{"\n+ "}}GROUP {{ or $g.GetXTitle $g.GetLabel | quote }} ({{$f.GetName}})

{{- with $g.GetScheduleChangesHtml }}
{{"  + "}}SCHEDULE CHANGES
{{ . | trim | wrap 120 | prefix "  | " }}
{{- end }}

{{- range $si, $s := .GetSchedules }}
{{"  &  "}}{{$s.GetCaption}} ({{$f.GetName}} > {{ or $g.GetXTitle $g.GetLabel }})
{{- range $sai, $sa := .GetActivities }}
{{- range $di, $d := .GetDays }}
{{- range $dti, $dt := .GetTimes }}
{{"    ~" -}}
{{" "}}[{{if $dt.HasXWkday}}{{wkdayshort $dt.GetXWkday | upper}}{{else}}{{index $s.GetDays $di}}{{end}} {{with (tr2cr $dt)}}{{.Format false}}{{else}}{{quote $dt.GetLabel}}{{end -}}]{{/**/ -}}
{{" "}}{{or $sa.GetXName $sa.GetLabel -}}
{{" "}}({{$f.GetName}}){{/**/ -}}
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
