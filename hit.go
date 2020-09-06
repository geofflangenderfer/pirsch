package pirsch

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var referrerQueryParams = []string{
	"ref",
	"referer",
	"referrer",
}

// Hit represents a single data point/page visit and is the central entity of pirsch.
type Hit struct {
	BaseEntity

	Fingerprint    string         `db:"fingerprint" json:"fingerprint"`
	Session        sql.NullTime   `db:"session" json:"session"`
	Path           sql.NullString `db:"path" json:"path,omitempty"`
	URL            sql.NullString `db:"url" json:"url,omitempty"`
	Language       sql.NullString `db:"language" json:"language,omitempty"`
	UserAgent      sql.NullString `db:"user_agent" json:"user_agent,omitempty"`
	Referrer       sql.NullString `db:"referrer" json:"referrer,omitempty"`
	OS             sql.NullString `db:"os" json:"os,omitempty"`
	OSVersion      sql.NullString `db:"os_version" json:"os_version,omitempty"`
	Browser        sql.NullString `db:"browser" json:"browser,omitempty"`
	BrowserVersion sql.NullString `db:"browser_version" json:"browser_version,omitempty"`
	Desktop        bool           `db:"desktop" json:"desktop"`
	Mobile         bool           `db:"mobile" json:"mobile"`
	Time           time.Time      `db:"time" json:"time"`
}

// String implements the Stringer interface.
func (hit Hit) String() string {
	out, _ := json.Marshal(hit)
	return string(out)
}

// HitOptions is used to manipulate the data saved on a hit.
type HitOptions struct {
	// TenantID is optionally saved with a hit to split the data between multiple tenants.
	TenantID sql.NullInt64

	// Path can be specified to manually overwrite the path stored for the request.
	// This will also affect the URL.
	Path string

	// ReferrerDomainBlacklist is used to filter out unwanted referrer from the Referrer header.
	// This can be used to filter out traffic from your own site or subdomains.
	// To filter your own domain and subdomains, add your domain to the list and set ReferrerDomainBlacklistIncludesSubdomains to true.
	// This way the referrer for blog.mypage.com -> mypage.com won't be saved.
	ReferrerDomainBlacklist []string

	// ReferrerDomainBlacklistIncludesSubdomains set to true to include all subdomains in the ReferrerDomainBlacklist,
	// or else subdomains must explicitly be included in the blacklist.
	// If the blacklist contains domain.com, sub.domain.com and domain.com will be treated as equally.
	ReferrerDomainBlacklistIncludesSubdomains bool

	// Session is the timestamp this fingerprint was first seen to identify the session.
	// Pass a zero time.Time to disable session tracking.
	Session time.Time
}

// HitFromRequest returns a new Hit for given request, salt and HitOptions.
// The salt must stay consistent to track visitors across multiple calls.
// The easiest way to track visitors is to use the Tracker.
func HitFromRequest(r *http.Request, salt string, options *HitOptions) Hit {
	now := time.Now().UTC() // capture first to get as close as possible

	// set default options in case they're nil
	if options == nil {
		options = &HitOptions{}
	}

	// manually overwrite path if set
	requestURL := r.URL.String()

	if options.Path != "" {
		u, err := url.Parse(r.RequestURI)

		if err == nil {
			// change path and re-assemble URL
			u.Path = options.Path
			requestURL = u.String()
		}
	} else {
		options.Path = r.URL.Path
	}

	// shorten strings if required and parse User-Agent to extract more data (OS, Browser)
	path := shortenString(options.Path, 2000)
	requestURL = shortenString(requestURL, 2000)
	ua := r.UserAgent()
	uaInfo := ParseUserAgent(ua)
	uaInfo.OS = shortenString(uaInfo.OS, 20)
	uaInfo.OSVersion = shortenString(uaInfo.OSVersion, 20)
	uaInfo.Browser = shortenString(uaInfo.Browser, 20)
	uaInfo.BrowserVersion = shortenString(uaInfo.BrowserVersion, 20)
	ua = shortenString(ua, 200)
	lang := shortenString(getLanguage(r), 10)
	referrer := shortenString(getReferrer(r, options.ReferrerDomainBlacklist, options.ReferrerDomainBlacklistIncludesSubdomains), 200)

	return Hit{
		BaseEntity:     BaseEntity{TenantID: options.TenantID},
		Fingerprint:    Fingerprint(r, salt),
		Session:        sql.NullTime{Time: options.Session, Valid: !options.Session.IsZero()},
		Path:           sql.NullString{String: path, Valid: path != ""},
		URL:            sql.NullString{String: requestURL, Valid: requestURL != ""},
		Language:       sql.NullString{String: lang, Valid: lang != ""},
		UserAgent:      sql.NullString{String: ua, Valid: ua != ""},
		Referrer:       sql.NullString{String: referrer, Valid: referrer != ""},
		OS:             sql.NullString{String: uaInfo.OS, Valid: uaInfo.OS != ""},
		OSVersion:      sql.NullString{String: uaInfo.OSVersion, Valid: uaInfo.OSVersion != ""},
		Browser:        sql.NullString{String: uaInfo.Browser, Valid: uaInfo.Browser != ""},
		BrowserVersion: sql.NullString{String: uaInfo.BrowserVersion, Valid: uaInfo.BrowserVersion != ""},
		Desktop:        uaInfo.IsDesktop(),
		Mobile:         uaInfo.IsMobile(),
		Time:           now,
	}
}

// IgnoreHit returns true, if a hit should be ignored for given request, or false otherwise.
// The easiest way to track visitors is to use the Tracker.
func IgnoreHit(r *http.Request) bool {
	// empty User-Agents are usually bots
	userAgent := strings.TrimSpace(strings.ToLower(r.Header.Get("User-Agent")))

	if userAgent == "" {
		return true
	}

	// ignore browsers pre-fetching data
	xPurpose := r.Header.Get("X-Purpose")
	purpose := r.Header.Get("Purpose")

	if r.Header.Get("X-Moz") == "prefetch" ||
		xPurpose == "prefetch" ||
		xPurpose == "preview" ||
		purpose == "prefetch" ||
		purpose == "preview" {
		return true
	}

	// filter for bot keywords
	for _, botUserAgent := range userAgentBlacklist {
		if strings.Contains(userAgent, botUserAgent) {
			return true
		}
	}

	return false
}

func getLanguage(r *http.Request) string {
	lang := r.Header.Get("Accept-Language")

	if lang != "" {
		langs := strings.Split(lang, ";")
		parts := strings.Split(langs[0], ",")
		return strings.ToLower(parts[0])
	}

	return ""
}

func getReferrer(r *http.Request, domainBlacklist []string, ignoreSubdomain bool) string {
	referrer := getReferrerFromHeaderOrQuery(r)

	if referrer == "" {
		return ""
	}

	u, err := url.Parse(referrer)

	if err != nil {
		return ""
	}

	hostname := u.Hostname()

	if ignoreSubdomain {
		hostname = stripSubdomain(hostname)
	}

	if containsString(domainBlacklist, hostname) {
		return ""
	}

	// remove query parameters and anchor
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func getReferrerFromHeaderOrQuery(r *http.Request) string {
	referrer := r.Header.Get("Referer")

	if referrer == "" {
		for _, param := range referrerQueryParams {
			referrer = r.URL.Query().Get(param)

			if referrer != "" {
				return referrer
			}
		}
	}

	return referrer
}

func stripSubdomain(hostname string) string {
	if hostname == "" {
		return ""
	}

	runes := []rune(hostname)
	index := len(runes) - 1
	dots := 0

	for i := index; i > 0; i-- {
		if runes[i] == '.' {
			dots++

			if dots == 2 {
				index++
				break
			}
		}

		index--
	}

	return hostname[index:]
}

func shortenString(str string, n int) string {
	// we intentionally use len instead of utf8.RuneCountInString here
	if len(str) > n {
		return str[:n]
	}

	return str
}
