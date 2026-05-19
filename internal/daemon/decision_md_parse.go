package daemon

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// parsedDecisionMD is the structured view of one markdown decision file.
// Fields that were absent from the file are nil — the import planner treats
// nil as "no update intended for this field" rather than "set to empty".
type parsedDecisionMD struct {
	LocalID      string  // required; comes from "# dec-N: ..." header
	NumericID    int64   // parsed from local_id for explicit-id insert
	Title        string  // required; comes from "# dec-N: <title>" header
	Rationale    *string // optional; "## Rationale" body
	Context      *string // optional; "## Context" body
	Consequences *string // optional; "## Consequences" body
	RevisedBy    string  // optional; "- Revised by: dec-N" metadata line
	Links        []parsedDecisionLink
}

type parsedDecisionLink struct {
	Relation   string
	TargetKind string
	TargetID   string
	Note       string
}

var (
	decisionMDHeaderRE   = regexp.MustCompile(`^#\s+(dec-(\d+))\s*:\s*(.+?)\s*$`)
	decisionMDMetaRE     = regexp.MustCompile(`^-\s+([A-Za-z][A-Za-z ]*?):\s*(.+?)\s*$`)
	decisionMDLinkRE     = regexp.MustCompile(`^-\s+(\S[^:—]*?)\s+(issue|requirement|decision):([^\s—]+)(?:\s+—\s+(.+?))?\s*$`)
	decisionMDSectionRE  = regexp.MustCompile(`^##\s+(\S.*?)\s*$`)
	decisionMDIncomingRE = regexp.MustCompile(`^-\s+\(incoming\)\s+`) // skip projections
)

// parseDecisionMarkdown converts a markdown file back into a structured
// decision. The parser is the inverse of renderDecisionMarkdown; the goal is
// that round-tripping content through render → parse → render is a fixed
// point.
func parseDecisionMarkdown(content []byte) (parsedDecisionMD, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var out parsedDecisionMD
	headerFound := false
	currentSection := ""
	var sectionBuf strings.Builder

	flushSection := func() {
		body := strings.TrimSpace(sectionBuf.String())
		sectionBuf.Reset()
		switch strings.ToLower(currentSection) {
		case "rationale":
			out.Rationale = strPtr(body)
		case "context":
			out.Context = strPtr(body)
		case "consequences":
			out.Consequences = strPtr(body)
		case "links":
			// Links section was already captured per-line below; the
			// section body itself is unused. Skip.
		case "":
			// Pre-section content (metadata block) is parsed line by line.
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimRight(line, " \t")

		// Header line: # dec-N: title
		if m := decisionMDHeaderRE.FindStringSubmatch(trimmed); m != nil {
			if headerFound {
				return parsedDecisionMD{}, fmt.Errorf("multiple decision headers found")
			}
			id, _ := strconv.ParseInt(m[2], 10, 64)
			out.LocalID = m[1]
			out.NumericID = id
			out.Title = strings.TrimSpace(m[3])
			headerFound = true
			continue
		}

		// Section header: ## Name
		if m := decisionMDSectionRE.FindStringSubmatch(trimmed); m != nil {
			flushSection()
			currentSection = strings.TrimSpace(m[1])
			continue
		}

		// Inside the Links section we parse each bullet directly.
		if strings.EqualFold(currentSection, "links") {
			if decisionMDIncomingRE.MatchString(trimmed) {
				continue // skip projection rows
			}
			if m := decisionMDLinkRE.FindStringSubmatch(trimmed); m != nil {
				out.Links = append(out.Links, parsedDecisionLink{
					Relation:   strings.TrimSpace(m[1]),
					TargetKind: strings.TrimSpace(m[2]),
					TargetID:   strings.TrimSpace(m[3]),
					Note:       strings.TrimSpace(m[4]),
				})
			}
			continue
		}

		// Pre-section metadata bullets (- Created: ..., - Revised by: ...)
		if currentSection == "" {
			if m := decisionMDMetaRE.FindStringSubmatch(trimmed); m != nil {
				key := strings.ToLower(strings.TrimSpace(m[1]))
				val := strings.TrimSpace(m[2])
				if key == "revised by" {
					out.RevisedBy = val
				}
			}
			// Other metadata (created/updated) is informational and ignored
			// on parse — the daemon owns those timestamps.
			continue
		}

		// Inside a body section: accumulate.
		sectionBuf.WriteString(trimmed)
		sectionBuf.WriteString("\n")
	}
	if err := scanner.Err(); err != nil {
		return parsedDecisionMD{}, fmt.Errorf("scan markdown: %w", err)
	}
	flushSection()

	if !headerFound {
		return parsedDecisionMD{}, fmt.Errorf("missing decision header (expected '# dec-N: <title>')")
	}
	if out.NumericID <= 0 {
		return parsedDecisionMD{}, fmt.Errorf("invalid decision id %q", out.LocalID)
	}
	if strings.TrimSpace(out.Title) == "" {
		return parsedDecisionMD{}, fmt.Errorf("missing decision title")
	}
	return out, nil
}

func strPtr(s string) *string {
	v := s
	return &v
}
