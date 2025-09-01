
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
