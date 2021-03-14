package core

import (
	"embed"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
	scraper "github.com/dranih/gowap/pkg/scraper"
	log "github.com/sirupsen/logrus"

	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary
var wg sync.WaitGroup

//go:embed assets/technologies.json
var f embed.FS

type scrapedURL struct {
	url    string
	status int
}

type temp struct {
	Apps       map[string]*jsoniter.RawMessage `json:"technologies"`
	Categories map[string]*jsoniter.RawMessage `json:"categories"`
}
type application struct {
	Name       string   `json:"name,ompitempty"`
	Version    string   `json:"version"`
	Categories []string `json:"categories,omitempty"`

	Cats     []int                             `json:"cats,omitempty"`
	Cookies  interface{}                       `json:"cookies,omitempty"`
	Dom      map[string]map[string]interface{} `json:"dom,omitempty"`
	Js       interface{}                       `json:"js,omitempty"`
	Headers  interface{}                       `json:"headers,omitempty"`
	HTML     interface{}                       `json:"html,omitempty"`
	Excludes interface{}                       `json:"excludes,omitempty"`
	Implies  interface{}                       `json:"implies,omitempty"`
	Meta     interface{}                       `json:"meta,omitempty"`
	Scripts  interface{}                       `json:"scripts,omitempty"`
	DNS      interface{}                       `json:"dns,omitempty"`
	URL      string                            `json:"url,omitempty"`
	Website  string                            `json:"website,omitempty"`
}

type category struct {
	Name     string `json:"name,omitempty"`
	Priority int    `json:"priority,omitempty"`
}

// Wappalyzer implements analyze method as original wappalyzer does
type Wappalyzer struct {
	Scraper    scraper.Scraper
	Apps       map[string]*application
	Categories map[string]*category
	JSON       bool
}

// Config for gowap
type Config struct {
	AppsJSONPath           string
	BrowserTimeoutSeconds  int
	NetworkTimeoutSeconds  int
	PageLoadTimeoutSeconds int
	JSON                   bool
	Scraper                string
}

// NewConfig struct with default values
func NewConfig() *Config {
	return &Config{AppsJSONPath: "", BrowserTimeoutSeconds: 4, NetworkTimeoutSeconds: 3, PageLoadTimeoutSeconds: 3, JSON: true, Scraper: "rod"}
}

// Init initializes wappalyzer
func Init(config *Config) (wapp *Wappalyzer, err error) {
	wapp = &Wappalyzer{}
	// Selecting scraper
	switch config.Scraper {
	case "colly":
		wapp.Scraper = &scraper.CollyScraper{BrowserTimeoutSeconds: config.BrowserTimeoutSeconds, NetworkTimeoutSeconds: config.NetworkTimeoutSeconds, PageLoadTimeoutSeconds: config.PageLoadTimeoutSeconds}
	case "rod":
		wapp.Scraper = &scraper.RodScraper{BrowserTimeoutSeconds: config.BrowserTimeoutSeconds, NetworkTimeoutSeconds: config.NetworkTimeoutSeconds, PageLoadTimeoutSeconds: config.PageLoadTimeoutSeconds}
	default:
		log.Errorf("Unknown scraper %s", config.Scraper)
		return wapp, errors.New("UnknownScraper")
	}

	err = wapp.Scraper.Init()

	var appsFile []byte
	if config.AppsJSONPath != "" {
		log.Infof("Trying to open technologies file at %s", config.AppsJSONPath)
		if _, err := os.Stat(config.AppsJSONPath); err == nil {
			appsFile, err = ioutil.ReadFile(config.AppsJSONPath)
			if err != nil {
				log.Errorf("Couldn't open file at %s\n", config.AppsJSONPath)
			} else {
				log.Infof("Technologies file opened")
			}
		} else {
			log.Errorf("Couldn't find file at %s\n", config.AppsJSONPath)
		}
	}
	if config.AppsJSONPath == "" || len(appsFile) == 0 {
		log.Infof("Loading included asset technologies.json")
		appsFile, err = f.ReadFile("assets/technologies.json")
		if err != nil {
			log.Errorf("Couldn't open included asset technologies.json\n")
			return nil, err
		}
	}

	temporary := &temp{}
	err = json.Unmarshal(appsFile, &temporary)
	if err != nil {
		log.Errorf("Couldn't unmarshal apps.json file: %s\n", err)
		return nil, err
	}
	wapp.Apps = make(map[string]*application)
	wapp.Categories = make(map[string]*category)
	for k, v := range temporary.Categories {
		catg := &category{}
		if err = json.Unmarshal(*v, catg); err != nil {
			log.Errorf("[!] Couldn't unmarshal Categories: %s\n", err)
			return nil, err
		}
		wapp.Categories[k] = catg
	}
	for k, v := range temporary.Apps {
		app := &application{}
		app.Name = k
		if err = json.Unmarshal(*v, app); err != nil {
			log.Errorf("Couldn't unmarshal Apps: %s\n", err)
			return nil, err
		}
		parseCategories(app, &wapp.Categories)
		wapp.Apps[k] = app
	}
	wapp.JSON = config.JSON
	return wapp, nil
}

type resultApp struct {
	Name       string   `json:"name,ompitempty"`
	Version    string   `json:"version"`
	Categories []string `json:"categories,omitempty"`
	Confidence int      `json:"confidence"`
	excludes   interface{}
	implies    interface{}
}

type detected struct {
	Mu   *sync.Mutex
	Apps map[string]*resultApp
}

// Analyze retrieves application stack used on the provided web-site
func (wapp *Wappalyzer) Analyze(paramURL string) (result interface{}, err error) {

	detectedApplications := &detected{new(sync.Mutex), make(map[string]*resultApp)}
	scraped, err := wapp.Scraper.Scrape(paramURL)
	res := map[string][]interface{}{}

	if !validateURL(paramURL) {
		log.Errorf("URL not valid : %s", paramURL)
		return res, errors.New("UrlNotValid")
	}

	canRenderPage := wapp.Scraper.CanRenderPage()

	for _, app := range wapp.Apps {
		wg.Add(1)
		go func(app *application) {
			defer wg.Done()
			analyzeURL(app, paramURL, detectedApplications)
			if canRenderPage && app.Js != nil {
				analyseJS(app, wapp.Scraper, detectedApplications)
			}
			if canRenderPage && app.Dom != nil {
				analyseDom(app, scraped.HTML, detectedApplications)
			}
			if app.HTML != nil {
				analyzeHTML(app, scraped.HTML, detectedApplications)
			}
			if len(scraped.Headers) > 0 && app.Headers != nil {
				analyzeHeaders(app, scraped.Headers, detectedApplications)
			}
			if len(scraped.Cookies) > 0 && app.Cookies != nil {
				analyzeCookies(app, scraped.Cookies, detectedApplications)
			}
			if len(scraped.Scripts) > 0 && app.Scripts != nil {
				analyzeScripts(app, scraped.Scripts, detectedApplications)
			}
			if len(scraped.Meta) > 0 && app.Meta != nil {
				analyzeMeta(app, scraped.Meta, detectedApplications)
			}
			if len(scraped.DNS) > 0 && app.DNS != nil {
				analyseDNS(app, scraped.DNS, detectedApplications)
			}
		}(app)
	}

	wg.Wait()

	for _, app := range detectedApplications.Apps {
		if app.excludes != nil {
			resolveExcludes(&detectedApplications.Apps, app.excludes)
		}
		if app.implies != nil {
			resolveImplies(&wapp.Apps, &detectedApplications.Apps, app.implies)
		}
	}

	for _, scrapedURL := range scraped.URLs {
		res["urls"] = append(res["urls"], map[string]interface{}{"url": scrapedURL.URL, "status": scrapedURL.Status})
	}
	for _, app := range detectedApplications.Apps {
		// log.Printf("URL: %-25s DETECTED APP: %-20s VERSION: %-8s CATEGORIES: %v", url, app.Name, app.Version, app.Categories)
		res["technologies"] = append(res["technologies"], map[string]interface{}{"name": app.Name, "confidence": app.Confidence, "version": app.Version, "categories": app.Categories})
	}
	if wapp.JSON {
		j, err := json.Marshal(res)
		if err != nil {
			return nil, err
		}
		return string(j), nil
	}
	return res, nil
}

func analyzeURL(app *application, url string, detectedApplications *detected) {
	patterns := parsePatterns(app.URL)
	for _, v := range patterns {
		for _, pattrn := range v {
			if pattrn.regex != nil && pattrn.regex.MatchString(url) {
				version := detectVersion(pattrn, &url)
				addApp(app, detectedApplications, version, pattrn.confidence)
			}
		}
	}
}

func analyzeScripts(app *application, scripts []string, detectedApplications *detected) {
	patterns := parsePatterns(app.Scripts)
	for _, v := range patterns {
		for _, pattrn := range v {
			if pattrn.regex != nil {
				for _, script := range scripts {
					if pattrn.regex.MatchString(script) {
						version := detectVersion(pattrn, &script)
						addApp(app, detectedApplications, version, pattrn.confidence)
					}
				}
			}
		}
	}
}

func analyzeHeaders(app *application, headers map[string][]string, detectedApplications *detected) {
	patterns := parsePatterns(app.Headers)
	for headerName, v := range patterns {
		headerNameLowerCase := strings.ToLower(headerName)
		for _, pattrn := range v {
			if headersSlice, ok := headers[headerNameLowerCase]; ok {
				for _, header := range headersSlice {
					if pattrn.str == "" || (pattrn.regex != nil && pattrn.regex.MatchString(header)) {
						version := detectVersion(pattrn, &header)
						addApp(app, detectedApplications, version, pattrn.confidence)
					}
				}
			}
		}
	}
}

func analyzeCookies(app *application, cookies map[string]string, detectedApplications *detected) {
	patterns := parsePatterns(app.Cookies)
	for cookieName, v := range patterns {
		cookieNameLowerCase := strings.ToLower(cookieName)
		for _, pattrn := range v {
			if cookie, ok := cookies[cookieNameLowerCase]; ok {
				if pattrn.str == "" || (pattrn.regex != nil && pattrn.regex.MatchString(cookie)) {
					version := detectVersion(pattrn, &cookie)
					addApp(app, detectedApplications, version, pattrn.confidence)
				}
			}
		}
	}
}

func analyzeHTML(app *application, html string, detectedApplications *detected) {
	patterns := parsePatterns(app.HTML)
	for _, v := range patterns {
		for _, pattrn := range v {
			if pattrn.regex != nil && pattrn.regex.MatchString(html) {
				version := detectVersion(pattrn, &html)
				addApp(app, detectedApplications, version, pattrn.confidence)
			}
		}

	}
}

func analyzeMeta(app *application, metas map[string][]string, detectedApplications *detected) {
	patterns := parsePatterns(app.Meta)
	for metaName, v := range patterns {
		metaNameLowerCase := strings.ToLower(metaName)
		for _, pattrn := range v {
			if metaSlice, ok := metas[metaNameLowerCase]; ok {
				for _, meta := range metaSlice {
					if pattrn.str == "" || (pattrn.regex != nil && pattrn.regex.MatchString(meta)) {
						version := detectVersion(pattrn, &meta)
						addApp(app, detectedApplications, version, pattrn.confidence)
					}
				}
			}
		}
	}
}

// analyseJS evals the JS properties and tries to match
func analyseJS(app *application, scraper scraper.Scraper, detectedApplications *detected) {
	patterns := parsePatterns(app.Js)
	for jsProp, v := range patterns {
		value, err := scraper.EvalJS(jsProp)
		if err == nil && value != nil {
			for _, pattrn := range v {
				if pattrn.str == "" || (pattrn.regex != nil && pattrn.regex.MatchString(*value)) {
					version := detectVersion(pattrn, value)
					addApp(app, detectedApplications, version, pattrn.confidence)
				}
			}
		}
	}
}

// analyseDom evals the DOM tries to match
func analyseDom(app *application, html string, detectedApplications *detected) {
	reader := strings.NewReader(html)
	doc, err := goquery.NewDocumentFromReader(reader)
	if err != nil {
		log.Fatal(err)
	}
	for domSelector, v1 := range app.Dom {
		doc.Find(domSelector).First().Each(func(i int, s *goquery.Selection) {
			for domType, v := range v1 {
				patterns := parsePatterns(v)
				for attribute, pattrns := range patterns {
					for _, pattrn := range pattrns {
						value := ""
						switch domType {
						case "text":
							value = s.Text()
						case "properties":
							// Not implemented, should be done into the browser to get element properties
							value, _ = s.Attr(attribute)
						case "attributes":
							value, _ = s.Attr(attribute)
						}
						if pattrn.str == "" || (pattrn.regex != nil && pattrn.regex.MatchString(value)) {
							version := detectVersion(pattrn, &value)
							addApp(app, detectedApplications, version, pattrn.confidence)
						}
					}
				}
			}
		})
	}
}

// analyseDNS tries to match dns records
func analyseDNS(app *application, dns map[string][]string, detectedApplications *detected) {
	patterns := parsePatterns(app.DNS)
	for dnsType, v := range patterns {
		dnsTypeUpperCase := strings.ToUpper(dnsType)
		for _, pattrn := range v {
			if dnsSlice, ok := dns[dnsTypeUpperCase]; ok {
				for _, dns := range dnsSlice {
					if pattrn.str == "" || (pattrn.regex != nil && pattrn.regex.MatchString(dns)) {
						version := detectVersion(pattrn, &dns)
						addApp(app, detectedApplications, version, pattrn.confidence)
					}
				}
			}
		}
	}
}

// addApp add a detected app to the detectedApplications
// if the app is already detected, we merge it (version, confidence, ...)
func addApp(app *application, detectedApplications *detected, version string, confidence int) {
	detectedApplications.Mu.Lock()
	if _, ok := (*detectedApplications).Apps[app.Name]; !ok {
		resApp := &resultApp{app.Name, version, app.Categories, confidence, app.Excludes, app.Implies}
		(*detectedApplications).Apps[resApp.Name] = resApp
	} else {
		if (*detectedApplications).Apps[app.Name].Version == "" {
			(*detectedApplications).Apps[app.Name].Version = version
		}
		if confidence > (*detectedApplications).Apps[app.Name].Confidence {
			(*detectedApplications).Apps[app.Name].Confidence = confidence
		}
	}
	detectedApplications.Mu.Unlock()
}

// detectVersion tries to extract version from value when app detected
func detectVersion(pattrn *pattern, value *string) (res string) {
	if pattrn.regex == nil {
		return ""
	}
	versions := make(map[string]interface{})
	version := pattrn.version
	if slices := pattrn.regex.FindAllStringSubmatch(*value, -1); slices != nil {
		for _, slice := range slices {
			for i, match := range slice {
				reg, _ := regexp.Compile(fmt.Sprintf("%s%d%s", "\\\\", i, "\\?([^:]+):(.*)$"))
				ternary := reg.FindAllString(version, -1)
				if ternary != nil && len(ternary) == 3 {
					version = strings.Replace(version, ternary[0], ternary[1], -1)
				}
				reg2, _ := regexp.Compile(fmt.Sprintf("%s%d", "\\\\", i))
				version = reg2.ReplaceAllString(version, match)
			}
		}
		if _, ok := versions[version]; ok != true && version != "" {
			versions[version] = struct{}{}
		}
		if len(versions) != 0 {
			for ver := range versions {
				if ver > res {
					res = ver
				}
			}
		}
	}
	return res
}

type pattern struct {
	str        string
	regex      *regexp.Regexp
	version    string
	confidence int
}

func parsePatterns(patterns interface{}) (result map[string][]*pattern) {
	parsed := make(map[string][]string)
	switch ptrn := patterns.(type) {
	case string:
		parsed["main"] = append(parsed["main"], ptrn)
	case map[string]interface{}:
		for k, v := range ptrn {
			switch content := v.(type) {
			case string:
				parsed[k] = append(parsed[k], v.(string))
			case []interface{}:
				for _, v1 := range content {
					parsed[k] = append(parsed[k], v1.(string))
				}
			default:
				log.Errorf("Unkown type in parsePatterns: %T\n", v)
			}
		}
	case []interface{}:
		var slice []string
		for _, v := range ptrn {
			slice = append(slice, v.(string))
		}
		parsed["main"] = slice
	default:
		log.Errorf("Unkown type in parsePatterns: %T\n", ptrn)
	}
	result = make(map[string][]*pattern)
	for k, v := range parsed {
		for _, str := range v {
			appPattern := &pattern{confidence: 100}
			slice := strings.Split(str, "\\;")
			for i, item := range slice {
				if item == "" {
					continue
				}
				if i > 0 {
					additional := strings.Split(item, ":")
					if len(additional) > 1 {
						if additional[0] == "version" {
							appPattern.version = additional[1]
						} else if additional[0] == "confidence" {
							appPattern.confidence, _ = strconv.Atoi(additional[1])
						}
					}
				} else {
					appPattern.str = item
					first := strings.Replace(item, `\/`, `/`, -1)
					second := strings.Replace(first, `\\`, `\`, -1)
					reg, err := regexp.Compile(fmt.Sprintf("%s%s", "(?i)", strings.Replace(second, `/`, `\/`, -1)))
					if err == nil {
						appPattern.regex = reg
					}
				}
			}
			result[k] = append(result[k], appPattern)
		}
	}
	return result
}

func resolveExcludes(detected *map[string]*resultApp, value interface{}) {
	patterns := parsePatterns(value)
	for _, v := range patterns {
		for _, excluded := range v {
			delete(*detected, excluded.str)
		}
	}
}

func resolveImplies(apps *map[string]*application, detected *map[string]*resultApp, value interface{}) {
	patterns := parsePatterns(value)
	for _, v := range patterns {
		for _, implied := range v {
			app, ok := (*apps)[implied.str]
			if _, ok2 := (*detected)[implied.str]; ok && !ok2 {
				resApp := &resultApp{app.Name, implied.version, app.Categories, implied.confidence, app.Excludes, app.Implies}
				(*detected)[implied.str] = resApp
				if app.Implies != nil {
					resolveImplies(apps, detected, app.Implies)
				}
			}
		}
	}
}

func parseCategories(app *application, categoriesCatalog *map[string]*category) {
	for _, categoryID := range app.Cats {
		app.Categories = append(app.Categories, (*categoriesCatalog)[strconv.Itoa(categoryID)].Name)
	}
}

func validateURL(url string) bool {
	regex, err := regexp.Compile(`^(?:http(s)?:\/\/)?[\w.-]+(?:\.[\w\.-]+)+[\w\-\._~:/?#[\]@!\$&'\(\)\*\+,;=.]+$`)
	ret := false
	if err == nil {
		ret = regex.MatchString(url)
	}
	return ret
}
