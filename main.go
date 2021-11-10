package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"

	"github.com/PagerDuty/go-pagerduty"
	"github.com/sensu-community/sensu-plugin-sdk/sensu"
	"github.com/sensu-community/sensu-plugin-sdk/templates"
	corev2 "github.com/sensu/sensu-go/api/core/v2"
)

type HandlerConfig struct {
	sensu.PluginConfig
	authToken         string
	dedupKeyTemplate  string
	statusMapJson     string
	summaryTemplate   string
	teamName          string
	teamSuffix        string
	detailsTemplate   string
	timestampTemplate string
	classTemplate     string
	groupTemplate     string
	sendDetailsAsJson bool
}

type eventStatusMap map[string][]uint32

var (
	config = HandlerConfig{
		PluginConfig: sensu.PluginConfig{
			Name:     "sensu-pagerduty-handler",
			Short:    "The Sensu Go PagerDuty handler for incident management",
			Keyspace: "sensu.io/plugins/sensu-pagerduty-handler/config",
		},
	}

	pagerDutyConfigOptions = []*sensu.PluginConfigOption{
		{
			Path:      "token",
			Env:       "PAGERDUTY_TOKEN",
			Argument:  "token",
			Shorthand: "t",
			Secret:    true,
			Usage:     "The PagerDuty V2 API authentication token, can be set with PAGERDUTY_TOKEN",
			Value:     &config.authToken,
			Default:   "",
		},
		{
			Path:     "team",
			Env:      "PAGERDUTY_TEAM",
			Argument: "team",
			Usage:    "Envvar name for pager team(alphanumeric and underscores) holding PagerDuty V2 API authentication token, can be set with PAGERDUTY_TEAM",
			Value:    &config.teamName,
			Default:  "",
		},
		{
			Path:     "team-suffix",
			Env:      "PAGERDUTY_TEAM_SUFFIX",
			Argument: "team-suffix",
			Usage:    "Pager team suffix string to append if missing from team name, can be set with PAGERDUTY_TEAM_SUFFIX",
			Value:    &config.teamSuffix,
			Default:  "_pagerduty_token",
		},
		{
			Path:      "dedup-key-template",
			Env:       "PAGERDUTY_DEDUP_KEY_TEMPLATE",
			Argument:  "dedup-key-template",
			Shorthand: "k",
			Usage:     "The PagerDuty V2 API deduplication key template, can be set with PAGERDUTY_DEDUP_KEY_TEMPLATE",
			Value:     &config.dedupKeyTemplate,
			Default:   "{{.Entity.Name}}-{{.Check.Name}}",
		},
		{
			Path:      "status-map",
			Env:       "PAGERDUTY_STATUS_MAP",
			Argument:  "status-map",
			Shorthand: "s",
			Usage:     "The status map used to translate a Sensu check status to a PagerDuty severity, can be set with PAGERDUTY_STATUS_MAP",
			Value:     &config.statusMapJson,
			Default:   "",
		},
		{
			Path:      "summary-template",
			Env:       "PAGERDUTY_SUMMARY_TEMPLATE",
			Argument:  "summary-template",
			Shorthand: "S",
			Usage:     "The template for the alert summary, can be set with PAGERDUTY_SUMMARY_TEMPLATE",
			Value:     &config.summaryTemplate,
			Default:   "{{.Entity.Name}}/{{.Check.Name}} : {{.Check.Output}}",
		},
		{
			Path:      "details-template",
			Env:       "PAGERDUTY_DETAILS_TEMPLATE",
			Argument:  "details-template",
			Shorthand: "d",
			Usage:     "The template for the alert details, can be set with PAGERDUTY_DETAILS_TEMPLATE (default full event JSON)",
			Value:     &config.detailsTemplate,
			Default:   "",
		},
		{
			Path:      "send-details-as-json",
			Env:       "PAGERDUTY_DETAILS_TEMPLATE",
			Argument:  "send-details-as-json",
			Usage:     "Indicates whether to process and send the details template as JSON as opposed to the default of text",
			Value:     &config.sendDetailsAsJson,
			Default:   false,
		},
		{
			Path:      "timestamp-template",
			Env:       "PAGERDUTY_TIMESTAMP_TEMPLATE",
			Argument:  "timestamp-template",
			Usage:     "The template for the PD-CEF Timestamp field, can be set with PAGERDUTY_TIMESTAMP_TEMPLATE (default \"\")",
			Value:     &config.timestampTemplate,
			Default:   "",
		},
		{
			Path:      "class-template",
			Env:       "PAGERDUTY_CLASS_TEMPLATE",
			Argument:  "class-template",
			Usage:     "The template for the PD-CEF Class field, can be set with PAGERDUTY_CLASS_TEMPLATE (default \"\")",
			Value:     &config.classTemplate,
			Default:   "",
		},
		{
			Path:      "group-template",
			Env:       "PAGERDUTY_GROUP_TEMPLATE",
			Argument:  "group-template",
			Usage:     "The template for the PD-CEF Group field, can be set with PAGERDUTY_GROUP_TEMPLATE (default \"\")",
			Value:     &config.groupTemplate,
			Default:   "",
		},
	}
)

func main() {
	goHandler := sensu.NewGoHandler(&config.PluginConfig, pagerDutyConfigOptions, checkArgs, manageIncident)
	goHandler.Execute()
}

func getTeamToken() (string, error) {
	//replace illegal characters
	reg, err := regexp.Compile("[^A-Za-z0-9]+")
	if err != nil {
		return "", err
	}
	//sanitize
	teamEnvVar := reg.ReplaceAllString(config.teamName, "_")
	teamEnvVarSuffix := reg.ReplaceAllString(config.teamSuffix, "_")
	//add suffix if needed
	if len(config.teamSuffix) > 0 {
		matched, err := regexp.MatchString(config.teamSuffix+"$", teamEnvVar)
		if err != nil {
			return "", err
		}
		if !matched {
			teamEnvVar = teamEnvVar + teamEnvVarSuffix
		}
	}
	if len(teamEnvVar) == 0 {
		return "", fmt.Errorf("unknown problem with team evironment variable")
	}
	log.Printf("Looking up token envvar: %s", teamEnvVar)
	teamToken := os.Getenv(teamEnvVar)
	if len(teamToken) == 0 {
		log.Printf("Token envvar %s is empty, using default token instead", teamEnvVar)
	} else {
		log.Printf("Token envvar %s found, replacing default token", teamEnvVar)
	}
	return teamToken, err
}

func checkArgs(event *corev2.Event) error {
	if !event.HasCheck() {
		return fmt.Errorf("event does not contain check")
	}
	if len(config.teamName) != 0 {
		teamToken, err := getTeamToken()
		if err != nil {
			return err
		}
		if len(teamToken) != 0 {
			config.authToken = teamToken
		}

	}
	if len(config.authToken) == 0 {
		return fmt.Errorf("no auth token provided")
	}
	return nil
}

func manageIncident(event *corev2.Event) error {
	severity, err := getPagerDutySeverity(event, config.statusMapJson)
	if err != nil {
		return err
	}
	log.Printf("Incident severity: %s", severity)

	summary, err := getSummary(event)
	if err != nil {
		return err
	}

	details, err := getDetails(event)
	if err != nil {
		return err
	}

	timestamp, err := getTimestamp(event)
	if err != nil {
		return err
	}

	class, err := getClass(event)
	if err != nil {
		return err
	}

	group, err := getGroup(event)
	if err != nil {
		return err
	}

	// "The maximum permitted length of PG event is 512 KB. Let's limit check output to 256KB to prevent triggering a failed send"
	if len(event.Check.Output) > 256000 {
		log.Printf("Warning Incident Payload Truncated!")
		event.Check.Output = "WARNING Truncated:i\n" + event.Check.Output[:256000] + "..."
	}

	// omitempty is used upstream so we can include Timestamp, Class and Group even if not set
	// https://github.com/PagerDuty/go-pagerduty/blob/d0d5cb1cff7f0344fbbc9f66fc9dfc603fa82a19/event_v2.go#L25-L34
	pdPayload := pagerduty.V2Payload{
		Source:    event.Entity.Name,
		Component: event.Check.Name,
		Severity:  severity,
		Summary:   summary,
		Details:   details,
		Timestamp: timestamp,
		Class:     class,
		Group:     group,
	}

	action := "trigger"

	if event.Check.Status == 0 {
		action = "resolve"
	}

	dedupKey, err := getPagerDutyDedupKey(event)
	if err != nil {
		return err
	}
	if len(dedupKey) == 0 {
		return fmt.Errorf("pagerduty dedup key is empty")
	}
	pdEvent := pagerduty.V2Event{
		RoutingKey: config.authToken,
		Action:     action,
		Payload:    &pdPayload,
		DedupKey:   dedupKey,
	}

	eventResponse, err := pagerduty.ManageEvent(pdEvent)
	if err != nil {
		log.Printf("Warning Event Send failed, sending fallback event\n %s", err.Error())
		failPayload := pagerduty.V2Payload{
			Source:    event.Entity.Name,
			Component: event.Check.Name,
			Severity:  severity,
			Summary:   summary,
			Details:   "Original payload had an error, maybe due to event length. PagerDuty Events must be less than 512KB",
		}
		failEvent := pagerduty.V2Event{
			RoutingKey: config.authToken,
			Action:     action,
			Payload:    &failPayload,
			DedupKey:   dedupKey,
		}

		failResponse, err := pagerduty.ManageEvent(failEvent)
		if err != nil {
			return err
		}
		// FUTURE send to AH
		log.Printf("Failback event (%s) submitted to PagerDuty, Status: %s, Dedup Key: %s, Message: %s", action, failResponse.Status, failResponse.DedupKey, failResponse.Message)
	}

	// FUTURE send to AH
	log.Printf("Event (%s) submitted to PagerDuty, Status: %s, Dedup Key: %s, Message: %s", action, eventResponse.Status, eventResponse.DedupKey, eventResponse.Message)
	return nil
}

func getPagerDutyDedupKey(event *corev2.Event) (string, error) {
	return templates.EvalTemplate("dedupKey", config.dedupKeyTemplate, event)
}

func getPagerDutySeverity(event *corev2.Event, statusMapJson string) (string, error) {
	var statusMap map[uint32]string
	var err error

	if len(statusMapJson) > 0 {
		statusMap, err = parseStatusMap(statusMapJson)
		if err != nil {
			return "", err
		}
	}

	if len(statusMap) > 0 {
		status := event.Check.Status
		severity := statusMap[status]
		if len(severity) > 0 {
			return severity, nil
		}
	}

	// Default to these values is no status map is found
	severity := "warning"
	if event.Check.Status < 3 {
		severities := []string{"info", "warning", "critical"}
		severity = severities[event.Check.Status]
	}

	return severity, nil
}

func parseStatusMap(statusMapJson string) (map[uint32]string, error) {
	validPagerDutySeverities := map[string]bool{"info": true, "critical": true, "warning": true, "error": true}

	statusMap := eventStatusMap{}
	err := json.Unmarshal([]byte(statusMapJson), &statusMap)
	if err != nil {
		return nil, err
	}

	// Reverse the map to key it on the status
	statusToSeverityMap := map[uint32]string{}
	for severity, statuses := range statusMap {
		if !validPagerDutySeverities[severity] {
			return nil, fmt.Errorf("invalid pagerduty severity: %s", severity)
		}
		for i := range statuses {
			statusToSeverityMap[uint32(statuses[i])] = severity
		}
	}

	return statusToSeverityMap, nil
}

func getSummary(event *corev2.Event) (string, error) {
	summary, err := templates.EvalTemplate("summary", config.summaryTemplate, event)
	if err != nil {
		return "", fmt.Errorf("failed to evaluate template %s: %v", config.summaryTemplate, err)
	}
	// "The maximum permitted length of this property is 1024 characters."
	if len(summary) > 1024 {
		summary = summary[:1024]
	}
	log.Printf("Incident Summary: %s", summary)
	return summary, nil
}

func getDetails(event *corev2.Event) (interface{}, error) {
	var (
		details     interface{}
		err         error
		detailsJson map[string]interface{}
	)

	if len(config.detailsTemplate) > 0 {
		details, err = templates.EvalTemplate("details", config.detailsTemplate, event)
		if err != nil {
			return "", fmt.Errorf("failed to evaluate template %s: %v", config.detailsTemplate, err)
		}
		if config.sendDetailsAsJson {
			json.Unmarshal([]byte(details.(string)), &detailsJson)
			return detailsJson, nil
		} else {
			return details, nil
		}
	} else {
		details = event
		return details, nil
	}
}

func getTimestamp(event *corev2.Event) (string, error) {
	var (
		timestamp string
		err       error
	)

	if len(config.timestampTemplate) > 0 {
		timestamp, err = templates.EvalTemplate("timestamp", config.timestampTemplate, event)
		if err != nil {
			return "", fmt.Errorf("failed to evaluate template %s: %v", config.timestampTemplate, err)
		}
	} else {
		timestamp = ""
	}

	log.Printf("Incident Timestamp: %s", timestamp)
	return timestamp, nil
}

func getClass(event *corev2.Event) (string, error) {
	var (
		class string
		err   error
	)

	if len(config.classTemplate) > 0 {
		class, err = templates.EvalTemplate("class", config.classTemplate, event)
		if err != nil {
			return "", fmt.Errorf("failed to evaluate template %s: %v", config.classTemplate, err)
		}
	} else {
		class = ""
	}
	log.Printf("Incident Class: %s", class)
	return class, nil
}

func getGroup(event *corev2.Event) (string, error) {
	var (
		group string
		err   error
	)

	if len(config.groupTemplate) > 0 {
		group, err = templates.EvalTemplate("group", config.groupTemplate, event)
		if err != nil {
			return "", fmt.Errorf("failed to evaluate template %s: %v", config.groupTemplate, err)
		}
	} else {
		group = ""
	}

	log.Printf("Incident Group: %s", group)
	return group, nil
}
