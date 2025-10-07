# Ottawa Rec Schedules

City of Ottawa drop-in recreation schedule data scraper.

I will be making a website for this soon. For now, you can use the data directly, or see [filter.go](./examples/filter.go) for a sample script to filter and display data.

### Usage

The data is updated daily. If you use the data, you must display the attribution text included in the data files.

| Format | Description | Download | Schema |
| --- | --- | --- | --- |
| JSON | Easiest to use for ad-hoc queries. Format may change over time. | [data.json](https://github.com/pgaskin/ottrec-data/raw/refs/heads/v1/data.json) | - |
| Protobuf | Best for integration with custom software. Most stable format. | [data.pb](https://github.com/pgaskin/ottrec-data/raw/refs/heads/v1/data.pb) | [data.proto](https://github.com/pgaskin/ottrec-data/raw/refs/heads/v1/data.proto) |
| TextPB | Best for manual inspection. Textual version of the protobuf. | [data.textpb](https://github.com/pgaskin/ottrec-data/raw/refs/heads/v1/data.textpb) | [data.proto](https://github.com/pgaskin/ottrec-data/raw/refs/heads/v1/data.proto) |

To view a diff of the schedule data over time, you can use the following command:

```bash
git remote add data https://github.com/pgaskin/ottrec-data
git fetch data
git \
  -c "diff.wsErrorHighlight=none" \
  -c "diff.context=3" \
  -c "diff.interHunkContext=0" \
  -c "diff.indentHeuristic=true" \
  -c "diff.orec_pb.cachetextconv=false" \
  -c "diff.orec_pb.xfuncname=^\s*[+&]\s*(.+)" \
  -c "diff.orec_pb.textconv=go run github.com/pgaskin/ottrec/textconv" \
  log --textconv -pw --patience data/v1 -- data.pb
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
  - Explicit reservation requirement in activity names as a boolean (typically, this is used as an exception to the default based on whether the schedule group has reservation links).
- Overlapping schedules (e.g., holiday schedules) are not merged. These schedules are not consistently formatted as they are manually named and created, so although I attempt to parse time ranges, I don't use them to merge schedules. This helps keep the scraper reliable and reduces the likelihood of accidentally missing important information.
- Any potential parsing problems are included in an array of error messages for each facility.
- A protobuf schema is used for maintainability, but it may be changed in backwards-incompatible ways if needed.

### Data changes

- **2025-10-07:** Changed time parsing logic to correctly handle time ranges with LHS 12h non-AM/PM AM time and RHS PM time (assume AM on the left instead of taking the PM from the right if it's less than 12h between). This fixes some issues with times like `11:45 - 1pm` being parsed as `23:45 - 13:00 tomorrow`. Note that this is quite rare, as they usually include the AM in the LHS time in this case, and they've only recently started making this typo. I've also added a warning to the scraper when a time spans into the next day, as this shouldn't usually happen as there aren't overnight activities.
- **2025-10-06:** Also parse schedule day header dates into `Schedule._daydates`.
- **2025-10-05:** Also parse top-level "reservation not required" text into `ScheduleGroup._noresv`.
- **2025-10-02:** Renamed from `orec2` to `ottrec`. Split data and cache into separate repository. Removed CSV export (will replace this with something better later).
- **2025-10-02:** Added support for scraping reservation links into `ScheduleGroup.reservation_links`, and parsing reservation requirement text in activity names into `Activity._resv`.
- **2025-09-16:** Switched to geocodio for geocoding. Facility longitude/latitude values are slightly different and generally more complete/accurate.
- **2025-09-04:** Significantly improved `ScheduleGroup._title` and `Activity._name` normalization.
- **2025-09-04:** Added new `Schedule._from` and `Schedule._to` parsed fields for the schedule date range.
- **2025-09-01:** Made the `TimeRange._start` and `TimeRange._end` parsing automatically correct unambiguous typos.
- **2025-09-01:** Run CSV export during daily updates.
- **2025-09-01:** Switched to opaque protobuf API and protobuf 2023. Some fields now use explicit field presence.
- **2025-04-18:** Initial release.
