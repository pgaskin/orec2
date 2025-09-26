# Ottawa Rec Schedules

City of Ottawa drop-in recreation schedule data scraper.

I will be making a website for this soon. For now, you can use the data directly, or see [filter.go](./examples/filter.go) for a sample script to filter and display data.

### Usage

The data is updated daily. If you use the data, you must display the attribution text included in the data files.

| Format | Description | Download | Schema |
| --- | --- | --- | --- |
| CSV | Easiest to use with existing software. Format may change over time. Lossy. | [data-csv.zip](https://github.com/pgaskin/orec2/archive/refs/heads/data-csv.zip) | [schema.ddl](https://github.com/pgaskin/orec2/raw/refs/heads/data-csv/schema.ddl) |
| JSON | Easiest to use for ad-hoc queries. Format may change over time. | [data.json](https://github.com/pgaskin/orec2/raw/refs/heads/data/data.json) | - |
| Protobuf | Best for integration with custom software. Most stable format. | [data.pb](https://github.com/pgaskin/orec2/raw/refs/heads/data/data.pb) | [data.proto](https://github.com/pgaskin/orec2/raw/refs/heads/data/data.proto) |
| TextPB | Best for manual inspection. Textual version of the protobuf. | [data.textpb](https://github.com/pgaskin/orec2/raw/refs/heads/data/data.textpb) | [data.proto](https://github.com/pgaskin/orec2/raw/refs/heads/data/data.proto) |

Historical data is available on the other git branches.

| Branch | Description |
| --- | --- |
| [cache](https://github.com/pgaskin/orec2/tree/cache) | Raw HTTP responses. Intended for troubleshooting and developing the scraper. |
| [data](https://github.com/pgaskin/orec2/tree/data) | Processed data. This is usually what you want to use. |
| [data-csv](https://github.com/pgaskin/orec2/tree/data-csv) | Processed data exported as CSV files. |

To view a diff of the schedule data over time, you can use the following command.

```bash
git \
  -c "diff.wsErrorHighlight=none" \
  -c "diff.context=3" \
  -c "diff.interHunkContext=0" \
  -c "diff.indentHeuristic=true" \
  -c "diff.orec_pb.cachetextconv=false" \
  -c "diff.orec_pb.xfuncname=^\s*[+&]\s*(.+)" \
  -c "diff.orec_pb.textconv=go run github.com/pgaskin/orec2/textconv" \
  log --textconv -pw --patience data -- data.pb
```

### Features and limitations

- Only basic facility information and schedule information are scraped. This helps keep the scraper reliable and ensures the schema can be kept stable long-term.
- Facility addresses are geocoded using geocodio (which has better results than pelias/geocode.earth and nominatim).
- Schedule changes and facility notifications are scraped on a best-effort basis without additional parsing since these fields are inherently free-form. This helps keep the scraper reliable and reduces the likelihood of accidentally missing important information.
- Scraped fields have minimal processing. This helps keep the scraper reliable and reduces the likelihood of accidentally missing important information.
- Optional fields are available which contain best-effort parsing and normalization of scraped fields (to assist with usage), including:
  - Normalized schedule group name.
  - Normalized schedule name (facility and date range stripped).
  - Raw schedule date range (if stripped from the normalized schedule name).
  - Parsed schedule date range.
  - Normalized schedule activity name.
  - Activity time range and weekday as an integer.
- Overlapping schedules (e.g., holiday schedules) are not merged. These schedules are not consistently formatted as they are manually named and created, so although I attempt to parse time ranges, I don't use them to merge schedules. This helps keep the scraper reliable and reduces the likelihood of accidentally missing important information.
- Any potential parsing problems are included in an array of error messages for each facility.
- A protobuf schema is used for maintainability, but it may be changed in backwards-incompatible ways if needed.

### Data changes

- **2025-09-16:** Switched to geocodio for geocoding. Facility longitude/latitude values are slightly different and generally more complete/accurate.
- **2025-09-04:** Significantly improved `ScheduleGroup._title` and `Activity._name` normalization.
- **2025-09-04:** Added new `Schedule._from` and `Schedule._to` parsed fields for the schedule date range.
- **2025-09-01:** Made the `TimeRange._start` and `TimeRange._end` parsing automatically correct unambiguous typos.
- **2025-09-01:** Run CSV export during daily updates.
- **2025-09-01:** Switched to opaque protobuf API and protobuf 2023. Some fields now use explicit field presence.
- **2025-04-18:** Initial release.
