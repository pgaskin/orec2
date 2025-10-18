# Ottawa Rec Schedules

City of Ottawa drop-in recreation schedule data scraper.

If you want to use the data, see [data.ottrec.ca](https://data.ottrec.ca/) for daily and historical data in various formats.

I will be making a website for this soon.

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

- **2025-10-17:** Added API and simplified CSV/JSON formats available via [data.ottrec.ca](https://data.ottrec.ca/).
- **2025-10-07:** Initial stable release.
