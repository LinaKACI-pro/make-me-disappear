package main

import (
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	domainsCSVURL = "https://raw.githubusercontent.com/your-digital-rights/yourdigitalrights.org/master/prod-domains.csv"
	apiBase       = "https://api.yourdigitalrights.org/domains/"
	workers       = 20
	outPath       = "brokers_scraped.yaml"
)

// apiResponse is the JSON structure from the YourDigitalRights API.
type apiResponse struct {
	StatusCode int `json:"statusCode"`
	Domain     struct {
		URL           string `json:"url"`
		Name          string `json:"name"`
		Email         string `json:"email"`
		PrivacyPolicy string `json:"privacyPolicy"`
		Headquarters  string `json:"headquarters"`
		Industries    string `json:"industries"`
		Specialties   string `json:"specialties"`
		CompanySize   string `json:"companySize"`
	} `json:"Domain"`
}

type broker struct {
	Slug       string
	Name       string
	Domain     string
	Email      string
	Region     string
	HQ         string
	Industries string
	PrivacyURL string
}

var (
	slugRe = regexp.MustCompile(`[^a-z0-9]+`)

	priorityKeywords = []string{
		"advertising", "marketing", "data", "analytics", "information",
		"technology", "internet", "software", "financial", "insurance",
		"credit", "telecom", "media",
	}

	privacyPrefixes = []string{
		"dpo@", "privacy@", "gdpr@", "dataprotection@", "rgpd@",
	}

	euMarkers = []string{
		"france", "paris", "germany", "berlin", "munich", "netherlands",
		"amsterdam", "belgium", "brussels", "spain", "madrid", "barcelona",
		"italy", "rome", "milan", "ireland", "dublin", "sweden", "stockholm",
		"denmark", "copenhagen", "finland", "helsinki", "austria", "vienna",
		"portugal", "lisbon", "poland", "warsaw", "czech", "prague",
		"luxembourg", "europe", "eu", "uk", "london", "united kingdom",
		"norway", "oslo", "switzerland", "zurich", "geneva",
	}

	usMarkers = []string{
		"united states", "usa", "new york", "san francisco", "los angeles",
		"chicago", "boston", "seattle", "austin", "denver", "atlanta",
		"california", "texas", "florida", "virginia", "washington",
	}
)

var client = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		MaxIdleConnsPerHost: workers,
	},
}

func main() {
	log.SetFlags(0)

	// Step 1: fetch domain list.
	log.Println("=== Fetching domain list ===")
	domains, err := fetchDomains()
	if err != nil {
		log.Fatalf("fetch domains: %v", err)
	}
	log.Printf("Found %d domains\n", len(domains))

	// Step 2: query API in parallel.
	log.Printf("\n=== Querying API (%d workers) ===\n", workers)
	brokers := queryAll(domains)
	log.Printf("\nKept %d brokers\n", len(brokers))

	// Step 3: sort (EU first) and write YAML.
	sortBrokers(brokers)
	if err := writeYAML(brokers); err != nil {
		log.Fatalf("write yaml: %v", err)
	}

	eu := 0
	withEmail := 0
	for _, b := range brokers {
		if b.Region == "EU" {
			eu++
		}
		if b.Email != "" {
			withEmail++
		}
	}

	log.Printf("\nWritten to %s", outPath)
	log.Printf("  %d brokers total (%d EU)", len(brokers), eu)
	log.Printf("  %d with email, %d without", withEmail, len(brokers)-withEmail)
	log.Println("\nNext steps:")
	log.Println("  1. Review brokers_scraped.yaml (EU brokers are at the top)")
	log.Println("  2. Remove brokers you don't need")
	log.Println("  3. cp brokers_scraped.yaml brokers.yaml")
	log.Println("  4. ./bot db-seed && ./bot campaign-init && ./bot run")
}

// fetchDomains downloads and parses the CSV domain list.
func fetchDomains() ([]string, error) {
	resp, err := client.Get(domainsCSVURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	r := csv.NewReader(resp.Body)
	// Skip header.
	if _, err := r.Read(); err != nil {
		return nil, err
	}

	var domains []string
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		d := strings.TrimSpace(record[0])
		if d != "" && strings.Contains(d, ".") {
			domains = append(domains, d)
		}
	}
	return domains, nil
}

// queryAll queries the API for all domains using a worker pool.
func queryAll(domains []string) []broker {
	type job struct {
		index  int
		domain string
	}

	jobs := make(chan job, len(domains))
	results := make(chan *broker, len(domains))

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				b := queryOne(j.domain)
				if b != nil {
					results <- b
				}
				if (j.index+1)%500 == 0 {
					log.Printf("  ... %d/%d queried", j.index+1, len(domains))
				}
			}
		}()
	}

	// Send all jobs.
	for i, d := range domains {
		jobs <- job{index: i, domain: d}
	}
	close(jobs)

	// Close results when all workers are done.
	go func() {
		wg.Wait()
		close(results)
	}()

	var brokers []broker
	for b := range results {
		brokers = append(brokers, *b)
	}
	return brokers
}

// queryOne queries the API for a single domain and returns a broker if it's interesting.
func queryOne(domain string) *broker {
	resp, err := client.Get(apiBase + domain)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return nil
	}

	var data apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	info := data.Domain
	email := strings.TrimSpace(strings.ToLower(info.Email))
	name := info.Name
	if name == "" {
		name = domain
	}

	if !isInteresting(info.Industries, info.Specialties, email) {
		return nil
	}

	return &broker{
		Slug:       slugify(name),
		Name:       name,
		Domain:     domain,
		Email:      email,
		Region:     guessRegion(info.Headquarters, info.Industries),
		HQ:         info.Headquarters,
		Industries: info.Industries,
		PrivacyURL: info.PrivacyPolicy,
	}
}

func isInteresting(industries, specialties, email string) bool {
	text := strings.ToLower(industries + " " + specialties)
	for _, kw := range priorityKeywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	for _, p := range privacyPrefixes {
		if strings.HasPrefix(email, p) {
			return true
		}
	}
	return false
}

func guessRegion(hq, industries string) string {
	text := strings.ToLower(hq + " " + industries)
	for _, m := range euMarkers {
		if strings.Contains(text, m) {
			return "EU"
		}
	}
	for _, m := range usMarkers {
		if strings.Contains(text, m) {
			return "US"
		}
	}
	return "GLOBAL"
}

func slugify(name string) string {
	s := slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	return strings.Trim(s, "-")
}

func sortBrokers(brokers []broker) {
	order := map[string]int{"EU": 0, "GLOBAL": 1, "US": 2}
	sort.Slice(brokers, func(i, j int) bool {
		oi, oj := order[brokers[i].Region], order[brokers[j].Region]
		if oi != oj {
			return oi < oj
		}
		return strings.ToLower(brokers[i].Name) < strings.ToLower(brokers[j].Name)
	})
}

func writeYAML(brokers []broker) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	eu := 0
	withEmail := 0
	for _, b := range brokers {
		if b.Region == "EU" {
			eu++
		}
		if b.Email != "" {
			withEmail++
		}
	}

	fmt.Fprintf(f, "# Auto-generated from YourDigitalRights.org API\n")
	fmt.Fprintf(f, "# Total: %d brokers (EU: %d)\n", len(brokers), eu)
	fmt.Fprintf(f, "# With email: %d | Without: %d\n", withEmail, len(brokers)-withEmail)
	fmt.Fprintf(f, "#\n")
	fmt.Fprintf(f, "# Review, then: cp brokers_scraped.yaml brokers.yaml && ./bot db-seed\n\n")
	fmt.Fprintf(f, "brokers:\n")

	seen := make(map[string]bool)
	for _, b := range brokers {
		slug := b.Slug
		if seen[slug] {
			slug = slug + "-" + strings.Split(b.Domain, ".")[0]
		}
		seen[slug] = true

		method := "email"
		if b.Email == "" {
			method = "manual"
		}

		fmt.Fprintf(f, "  - id: %q\n", slug)
		fmt.Fprintf(f, "    name: %q\n", b.Name)
		fmt.Fprintf(f, "    region: %q\n", b.Region)
		fmt.Fprintf(f, "    method: %q\n", method)
		if b.Email != "" {
			fmt.Fprintf(f, "    contact: %q\n", b.Email)
		}
		if b.PrivacyURL != "" {
			fmt.Fprintf(f, "    opt_out_url: %q\n", b.PrivacyURL)
		}
		fmt.Fprintf(f, "    tier: 2\n")

		var notes []string
		if b.HQ != "" {
			notes = append(notes, b.HQ)
		}
		if b.Industries != "" {
			notes = append(notes, b.Industries)
		}
		if b.Email == "" {
			notes = append(notes, "no email found")
		}
		if len(notes) > 0 {
			fmt.Fprintf(f, "    notes: %q\n", strings.Join(notes, "; "))
		}
		fmt.Fprintln(f)
	}

	return nil
}
