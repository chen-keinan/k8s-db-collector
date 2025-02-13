package cve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	version "github.com/aquasecurity/go-pep440-version"
	"github.com/aquasecurity/k8s-db-collector/collectors/cvedb/utils"
	"github.com/hashicorp/go-multierror"
)

const (
	k8svulnDBURL = "https://kubernetes.io/docs/reference/issues-security/official-cve-feed/index.json"
	mitreURL     = "https://cveawg.mitre.org/api/cve"
	cveList      = "https://www.cve.org/"
	semver       = "SEMVER"
)

func Collect() (*K8sVulnDB, error) {
	response, err := http.Get(k8svulnDBURL)
	if err != nil {
		return nil, err
	}
	vulnDB, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	return ParseVulnDBData(vulnDB)
}

const (
	// Kubernetes is a container orchestration system for Docker containers
	excludeNonCoreComponentsCves = "CVE-2019-11255,CVE-2020-10749,CVE-2020-8554"
)

func ParseVulnDBData(vulnDB []byte) (*K8sVulnDB, error) {
	var db map[string]interface{}
	err := json.Unmarshal(vulnDB, &db)
	if err != nil {
		return nil, err
	}
	fullVulnerabilities := make([]*Vulnerability, 0)
	for _, item := range db["items"].([]interface{}) {
		i := item.(map[string]interface{})
		id := i["id"].(string)
		if strings.Contains(excludeNonCoreComponentsCves, id) {
			continue
		}
		externalURL := i["external_url"].(string)
		for _, cveID := range utils.GetMultiIDs(id) {
			vulnerability, err := parseMitreCve(externalURL, cveID)
			if err != nil || len(vulnerability.Component) == 0 {
				continue
			}
			if len(vulnerability.AffectedVersions) == 0 {
				continue
			}
			contentText := i["content_text"].(string)
			component := utils.GetComponentFromDescriptionAndffected(contentText)

			fullVulnerabilities = append(fullVulnerabilities, &Vulnerability{
				ID:          cveID,
				CreatedAt:   i["date_published"].(string),
				Component:   getComponentName(component, vulnerability),
				Affected:    GetAffectedEvents(vulnerability),
				Summary:     i["summary"].(string),
				Description: vulnerability.Description,
				Urls:        []string{i["url"].(string), externalURL},
				CvssV3:      vulnerability.CvssV3,
				Severity:    vulnerability.Severity,
			})
		}
	}
	err = ValidateCveData(fullVulnerabilities)
	if err != nil {
		return nil, err
	}
	return &K8sVulnDB{fullVulnerabilities}, nil
}

func GetAffectedEvents(v *Vulnerability) []*Affected {
	affected := make([]*Affected, 0)
	for _, av := range v.AffectedVersions {
		if len(av.Introduced) == 0 {
			continue
		}
		if av.Introduced == "0.0.0" {
			av.Introduced = "0"
		}
		events := make([]*Event, 0)
		ranges := make([]*Range, 0)
		if len(av.Introduced) > 0 {
			events = append(events, &Event{Introduced: av.Introduced})
		}
		if len(av.Fixed) > 0 {
			events = append(events, &Event{Fixed: av.Fixed})
		}
		if len(av.LastAffected) > 0 && len(av.Fixed) == 0 {
			events = append(events, &Event{LastAffected: av.LastAffected})
		}
		if len(av.Introduced) > 0 && len(av.LastAffected) == 0 && len(av.Fixed) == 0 {
			events = append(events, &Event{LastAffected: av.Introduced})
		}
		ranges = append(ranges, &Range{
			RangeType: semver,
			Events:    events,
		})
		affected = append(affected, &Affected{Ranges: ranges})
	}
	return affected
}

func getComponentName(k8sComponent string, mitreCve *Vulnerability) string {
	// prefer mitre component if exists
	if len(mitreCve.Component) != 0 && strings.ToLower(mitreCve.Component) != "kubernetes" {
		k8sComponent = mitreCve.Component
	}
	upstreamPrefix := utils.UpstreamOrgByName(k8sComponent)
	if upstreamPrefix != "" {
		return strings.ToLower(fmt.Sprintf("%s/%s", upstreamPrefix, utils.UpstreamRepoByName(k8sComponent)))
	}
	av := utils.UpstreamOrgByName(mitreCve.Component)
	return strings.ToLower(fmt.Sprintf("%s/%s", av, utils.UpstreamRepoByName(mitreCve.Component)))
}

func ValidateCveData(cves []*Vulnerability) error {
	var result error
	for _, cve := range cves {
		if len(cve.ID) == 0 {
			result = multierror.Append(result, fmt.Errorf("\nid is mssing on cve #%s", cve.ID))
		}
		if len(cve.CreatedAt) == 0 {
			result = multierror.Append(result, fmt.Errorf("\nCreatedAt is mssing on cve #%s", cve.ID))
		}
		if len(cve.Summary) == 0 {
			result = multierror.Append(result, fmt.Errorf("\nSummary is mssing on cve #%s", cve.ID))
		}
		if len(strings.TrimPrefix(cve.Component, utils.UpstreamOrgByName(cve.Component))) == 0 {
			result = multierror.Append(result, fmt.Errorf("\nComponent is mssing on cve #%s", cve.ID))
		}
		if len(cve.Description) == 0 {
			result = multierror.Append(result, fmt.Errorf("\nDescription is mssing on cve #%s", cve.ID))
		}
		if len(cve.Affected) == 0 {
			result = multierror.Append(result, fmt.Errorf("\nFixedVersion is missing on cve #%s", cve.ID))
		}
		if len(cve.Affected) > 0 {
			for _, v := range cve.AffectedVersions {
				_, err := version.Parse(v.Introduced)
				if err != nil {
					result = multierror.Append(result, fmt.Errorf("\nAffectedVersion From %s is invalid on cve #%s", v.Introduced, cve.ID))
				}
			}
		}
		if cve.CvssV3.Score == 0 {
			result = multierror.Append(result, fmt.Errorf("\nVector is mssing on cve #%s", cve.ID))
		}
		if cve.CvssV3.Vector == "" {
			result = multierror.Append(result, fmt.Errorf("\nVector is mssing on cve #%s", cve.ID))
		}
		if cve.Severity == "" {
			result = multierror.Append(result, fmt.Errorf("\nSeverity is mssing on cve #%s", cve.ID))
		}
		if len(cve.Urls) == 0 {
			result = multierror.Append(result, fmt.Errorf("\nUrls is mssing on cve #%s", cve.ID))
		}
	}
	return result
}
