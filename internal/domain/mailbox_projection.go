package domain

// CanonicalMailboxProducerPayload removes fields derived from the immutable
// mailbox body. These indexes may evolve with parsers and validation schemas;
// they are not part of a mailbox event's producer-authored identity.
func CanonicalMailboxProducerPayload(payload map[string]any) map[string]any {
	canonical := make(map[string]any, len(payload))
	for key, value := range payload {
		switch key {
		case "worker_evidence", "worker_evidence_validation":
			continue
		default:
			canonical[key] = value
		}
	}
	if len(canonical) == 0 {
		return nil
	}
	return canonical
}
