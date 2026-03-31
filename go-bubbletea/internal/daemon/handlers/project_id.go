package handlers

import (
	"strings"

	"github.com/riordanpawley/azedarach/internal/contracts/protocol"
)

func resolveProjectID(bodyProjectID string, meta protocol.Metadata) string {
	projectID := strings.TrimSpace(bodyProjectID)
	if projectID != "" {
		return projectID
	}
	projectID = strings.TrimSpace(meta.ProjectID)
	return projectID
}
