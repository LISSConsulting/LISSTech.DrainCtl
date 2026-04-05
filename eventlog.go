//go:build windows

package drainctl

import (
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// wevtutil XML response structures for Event ID 4657
// (Audit Registry Value Modified)

type eventXML struct {
	XMLName xml.Name   `xml:"Events"`
	Events  []eventRec `xml:"Event"`
}

type eventRec struct {
	System    eventSystem    `xml:"System"`
	EventData eventDataItems `xml:"EventData"`
}

type eventSystem struct {
	TimeCreated struct {
		SystemTime string `xml:"SystemTime,attr"`
	} `xml:"TimeCreated"`
}

type eventDataItems struct {
	Data []eventDataItem `xml:"Data"`
}

type eventDataItem struct {
	Name  string `xml:"Name,attr"`
	Value string `xml:",chardata"`
}

func (e *eventRec) dataValue(name string) string {
	for _, d := range e.EventData.Data {
		if d.Name == name {
			return d.Value
		}
	}
	return ""
}

// QueryRegistryChangeUser queries the Windows Security Event Log for recent
// Event ID 4657 events referencing the Terminal Server registry key.
// Returns the username from the most recent matching event, or "" if none found.
func QueryRegistryChangeUser(since time.Time) string {
	sinceUTC := since.UTC().Format("2006-01-02T15:04:05.000Z")

	xpath := fmt.Sprintf(
		"*[System[EventID=4657 and TimeCreated[@SystemTime>='%s']]]",
		sinceUTC,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "wevtutil", "qe", "Security",
		"/q:"+xpath,
		"/c:20",
		"/f:xml",
		"/rd:true",
	)

	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	wrapped := "<Events>" + string(out) + "</Events>"

	var events eventXML
	if err := xml.Unmarshal([]byte(wrapped), &events); err != nil {
		return ""
	}

	// The event log records the real hive path (ControlSet001/002), not
	// the CurrentControlSet symlink. Match on the tail of the path.
	// OperationType is the raw message ID %%1905, not rendered text.
	const tsSuffix = `\control\terminal server`
	for _, evt := range events.Events {
		objectName := strings.ToLower(evt.dataValue("ObjectName"))
		valueName := evt.dataValue("ObjectValueName")

		if strings.HasSuffix(objectName, tsSuffix) && strings.EqualFold(valueName, "TSServerDrainMode") {
			subjectUser := evt.dataValue("SubjectUserName")
			subjectDomain := evt.dataValue("SubjectDomainName")

			if subjectUser != "" {
				if subjectDomain != "" && strings.ToUpper(subjectDomain) != "NT AUTHORITY" {
					return subjectDomain + `\` + subjectUser
				}
				return subjectUser
			}
		}
	}

	return ""
}
