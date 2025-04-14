# Ottawa Rec Schedules

Scraper and website for viewing and filtering City of Ottawa drop-in recreation schedules.

The cached pages and geocoding data can be found on the [cache](https://github.com/pgaskin/orec2/tree/cache) branch. The parsed data can be found on the [data](https://github.com/pgaskin/orec2/tree/data) branch. The data is updated daily.

The website is coming soon.

**Why I made this:**

- The list of facilities on the Ottawa website has addresses, but no map, making it hard to visualize the location with regards to transit.
- The schedules on the Ottawa website are all on their own pages within collapsible accordions, making it hard to skim multiple schedules quickly.
- There is no way to filter for all facilities which currently have a specific activity scheduled.
- There is no way to filter activities by time or day.
- There are no notifications for schedule changes.

**Features and limitations of scraped data:**

- Only basic facility information, longitude/latitude, and schedule information is scraped.
- Schedule changes and facility notifications are scraped on a best-effort basis without additional parsing.
- Scraped fields have minimal processing for maintainability and to avoid lossiness.
- Optional fields are available which contain best-effort parsing and normalization of scraped fields, including:
  - Normalized schedule group name.
  - Normalized schedule activity name.
  - Activity time range and weekday as an integer.
- Overlapping schedules (e.g., holiday schedules) are not merged. These schedules are not consistently formatted as they are manually named and created, so I don't attempt to parse schedule time ranges. It is easier to just show everything and leave it to reader to decide.
- Any potential parsing problems are included in an array of error messages for each facility.
- A protobuf schema is used for maintainability, but it may be changed in backwards-incompatible ways if needed.

**Similar things:**

- [ottawapublicskating.ca](https://www.ottawapublicskating.ca/) is only for skating, has been inaccurate at times, has limited detail, and doesn't show schedule changes.
- [claudielarouche.com/skating.html](https://claudielarouche.com/skating.html) and [claudielarouche.com/swim.html](https://claudielarouche.com/swim.html) have drop-in swimming and skating times, seems mostly okay, but is hard to skim and doesn't show schedule changes.
