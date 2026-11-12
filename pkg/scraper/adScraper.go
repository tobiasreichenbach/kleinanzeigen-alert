package scraper

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	neturl "net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const url = "https://www.kleinanzeigen.de/seite:%v/s-%s/k0l%vr%v"

const cityURL = "https://www.kleinanzeigen.de/s-ort-empfehlungen.json?query=%s"

// Ad is a representation of the kleinanzeigen ads
type Ad struct {
	Title    string
	Link     string
	Price    string
	Location string
	ID       string
}

// searchResult mirrors the relevant parts of the JSON document kleinanzeigen
// returns for search result pages.
type searchResult struct {
	Result struct {
		ResultList []struct {
			OrganicAdPreview *struct {
				ID                 int64  `json:"id"`
				Title              string `json:"title"`
				SeoLink            string `json:"seoLink"`
				Price              string `json:"price"`
				LocationName       string `json:"locationName"`
				ParentLocationName string `json:"parentLocationName"`
				TopAd              bool   `json:"topAd"`
			} `json:"organicAdPreview"`
		} `json:"resultList"`
	} `json:"result"`
}

// GetAds gets the ads for the specified page serachterm citycode and radius
func GetAds(page int, term string, cityCode int, radius int, maxPrice *int, minPrice *int, customLink *string) ([]Ad, error) {
	log.Debug().Msg("scraping for ads")
	query := fmt.Sprintf(url, page, strings.ReplaceAll(term, " ", "-"), cityCode, radius)

	if (customLink != nil) && CheckUrl(*customLink) {
		query = jsonLink(*customLink)
	}

	ads := make([]Ad, 0)

	client := http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, query, nil)
	if err != nil {
		return ads, err
	}
	req.Header.Set("User-Agent", "telegram-alert-bot/1.0")

	res, err := client.Do(req)
	if err != nil {
		log.Error().Err(err).Str("term", term).Int("radius", radius).Msg("error while scraping for ads")
		return ads, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		log.Error().Str("status_code", res.Status).Str("term", term).Msg("error while scraping for ads")
		if res.StatusCode == http.StatusForbidden {
			return ads, errors.New("Forbidden")
		}
		return ads, fmt.Errorf("unexpected status %s", res.Status)
	}

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return ads, err
	}

	var result searchResult
	if err := json.Unmarshal(body, &result); err != nil {
		log.Error().Err(err).Str("query", query).Msg("could not parse search result, page format might have changed")
		return ads, errors.New("could not parse search result")
	}

	space := regexp.MustCompile(`\s+`)

	for _, entry := range result.Result.ResultList {
		ad := entry.OrganicAdPreview
		if ad == nil || ad.TopAd || ad.ID == 0 {
			continue
		}

		price := strings.TrimSpace(ad.Price)

		if !priceInRange(price, maxPrice, minPrice) {
			continue
		}

		location := space.ReplaceAllString(strings.TrimSpace(ad.LocationName+" "+ad.ParentLocationName), " ")

		ads = append(ads, Ad{
			Title:    ad.Title,
			Link:     "https://www.kleinanzeigen.de" + ad.SeoLink,
			ID:       strconv.FormatInt(ad.ID, 10),
			Price:    price,
			Location: location,
		})
	}

	log.Debug().Str("query", term).Int("number_of_ads", len(ads)).Msg("scraped ads for query")

	return ads, nil
}

// jsonLink rewrites a search link so kleinanzeigen answers with JSON instead of HTML.
// Search pages are served as JSON when the path starts with /seite:{n}/.
func jsonLink(link string) string {
	link = strings.TrimSpace(link)
	const prefix = "https://www.kleinanzeigen.de/"
	if strings.HasPrefix(link, prefix) && !strings.HasPrefix(link, prefix+"seite:") {
		return prefix + "seite:1/" + strings.TrimPrefix(link, prefix)
	}
	return link
}

// priceInRange applies the optional max/min price filters (same semantics as before:
// filters only apply when a max price is set; "zu verschenken" always passes).
func priceInRange(price string, maxPrice *int, minPrice *int) bool {
	if maxPrice == nil || strings.ToLower(price) == "zu verschenken" {
		return true
	}

	replaced := strings.TrimSpace(strings.NewReplacer("VB", "", "€", "", ".", "").Replace(price))
	if len(replaced) == 0 {
		return false
	}

	priceValue, err := strconv.Atoi(replaced)
	if err != nil {
		log.Warn().Str("price-string", replaced).Msg("could not parse price from ad")
		return false
	}

	if priceValue >= *maxPrice {
		return false
	}

	if minPrice != nil && priceValue < *minPrice {
		return false
	}

	return true
}

// FindCityID finds the city by the name/postal code.
// "Deutschland" (or "de", "0", "alle") searches all of Germany (location id 0).
func FindCityID(untrimmed string) (int, string, error) {
	log.Debug().Str("city_search_term", untrimmed).Msg("finding city id")

	city := strings.TrimSpace(untrimmed)

	switch strings.ToLower(city) {
	case "", "deutschland", "ganz deutschland", "de", "0", "alle", "überall":
		return 0, "Deutschland", nil
	}

	client := http.Client{
		Timeout: time.Second * 5,
	}

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf(cityURL, neturl.QueryEscape(city)), nil)

	if err != nil {
		log.Error().Err(err).Msg("could not create the request")
		return 0, "", errors.New("could not make request")
	}

	req.Header.Set("Accept", "application/json")

	res, getErr := client.Do(req)

	if getErr != nil {
		return 0, "", errors.New("could not send request")
	}
	defer res.Body.Close()

	if res.StatusCode != 200 {
		log.Error().Str("status_code", res.Status).Msg("received a wrong status code.")
		if res.StatusCode == 403 {
			log.Error().Msg("ip address might be blocked by kleinanzeigen.")
		}
		return 0, "", errors.New("request for city not successful")
	}

	// The response is an ordered JSON object ("_<id>": "<name>"), best match first.
	// A plain map would lose that order, so read it token by token.
	dec := json.NewDecoder(res.Body)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return 0, "", errors.New("could not parse json")
	}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return 0, "", errors.New("could not parse json")
		}
		var name string
		if err := dec.Decode(&name); err != nil {
			return 0, "", errors.New("could not parse json")
		}

		key, _ := keyTok.(string)
		cityID, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(key, "_")))
		if err != nil || cityID == 0 {
			// skip the generic "Deutschland" entry that is always returned first
			continue
		}

		log.Debug().Int("city_id", cityID).Str("city_name", name).Msg("found city")
		return cityID, name, nil
	}

	return 0, "", errors.New("could not find city")
}

// CheckUrl checks if a url is valid
func CheckUrl(untrimmed string) bool {
	url := strings.Trim(untrimmed, " ")

	var urlRegex = regexp.MustCompile(`https://www.kleinanzeigen.de/s-[^? ]+`)

	return urlRegex.Match([]byte(url))
}
