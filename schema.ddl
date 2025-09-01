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
