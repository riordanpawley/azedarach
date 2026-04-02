package handlers

import (
	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func resolveProjectID(bodyProjectID string, meta protocol.Metadata) string {
	projectID := protocol.TrimProjectID(bodyProjectID)
	if projectID != "" {
		return projectID
	}
	projectID = protocol.TrimProjectID(meta.ProjectID)
	if projectID != "" {
		return projectID
	}
	return protocol.NormalizeProjectID("")
}
