/** @jsx h */
/** @jsxFrag Fragment */
import { h, Fragment } from 'preact'
import { render } from 'preact-render-to-string/jsx'

import data from "../data/data.json"

const schedule = s =>
    <table class="schedule" data-schedule={s._name}>
        <caption>{s.caption}</caption>
        <thead>
            <tr>
                <th></th>
                {s.days.map(x => <th>{x}</th>)}
            </tr>
        </thead>
        <tbody>
            {s.activities.map(sa =>
                <tr data-activity={sa._name}>
                    <th>{sa.label}</th>
                    {sa.days.map(sd =>
                        <td>
                            {sd.times.map(st =>
                                <span data-t1={`${st._start}`} data-t2={`${st._end}`} data-wd={`${st._wkday}`}>{st.label.replace(/\s+/g, "")}</span>
                            )}
                        </td>
                    )}
                </tr>
            )}
        </tbody>
    </table>

const facility = f =>
    <article class="facility" data-facility={f.name} data-lng={`${f._lnglat?.lng}`} data-lat={`${f._lnglat?.lat}`}>
        <h2><a href={f.source.url}>{f.name}</a></h2>
        <aside>
            <div>{f.source._date}</div>
            <div>{f.address}</div>
            <div dangerouslySetInnerHTML={{__html: f.specialHoursHtml}}></div>
            <div dangerouslySetInnerHTML={{__html: f.notificationsHtml}}></div>
        </aside>
        {f.scheduleGroups.map(sg => 
            <section class="schedule-group" data-schedule-group={sg._title}>
                <h3>{sg.label}</h3>
                <div dangerouslySetInnerHTML={{__html: sg.scheduleChangesHtml}}></div>
                {sg.schedules.map(schedule)}
            </section>
        )}
        <footer>
            {f._errors.map(err => <div class="error">{err}</div>)}
        </footer>
    </article>

export default ({
    bundleJS = "bundle.js",
    bundleCSS = "bundle.css",
} = {}) => `<!DOCTYPE html>` + render((
    <html>
        <head>
            <meta charset="UTF-8"/>
            <meta name="viewport" content="width=device-width, initial-scale=1.0"/>
            <title>Ottawa Rec Schedules</title>
            <link rel="stylesheet" href={bundleCSS}/>
        </head>
        <body>
            {data.facilities.map(facility)}
            <footer>
                {data.attribution.map(x => <p>{x}</p>)}
            </footer>
            <script src={bundleJS}></script>
        </body>
    </html>
), {}, { pretty: false })
