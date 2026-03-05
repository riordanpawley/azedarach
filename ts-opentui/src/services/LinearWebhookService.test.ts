import { LinearWebhookClient } from "@linear/sdk/webhooks"
import { describe, expect, it } from "bun:test"
import * as crypto from "node:crypto"
import { Option } from "effect"
import { decodeLinearIssueWebhookEvent } from "./LinearWebhookService.js"

const issueWebhookPayload = {
	type: "Issue",
	action: "create",
	data: {
		id: "7d9f958e-3700-4fdf-b82c-9e9b86276d40",
		identifier: "AZE-123",
		title: "Webhook test issue",
		description: "Test payload",
		priority: 2,
		createdAt: "2026-03-05T00:00:00.000Z",
		updatedAt: "2026-03-05T00:00:00.000Z",
		completedAt: null,
		canceledAt: null,
		parentId: null,
		teamId: "team-123",
		state: {
			id: "state-1",
			name: "Backlog",
			type: "unstarted",
		},
		labels: [
			{
				id: "label-1",
				name: "type:feature",
			},
		],
		url: "https://linear.app/example/issue/AZE-123",
	},
}

describe("decodeLinearIssueWebhookEvent", () => {
	it("decodes Issue payloads", () => {
		const decoded = decodeLinearIssueWebhookEvent(issueWebhookPayload)
		expect(Option.isSome(decoded)).toBe(true)
		if (Option.isSome(decoded)) {
			expect(decoded.value.data.identifier).toBe("AZE-123")
		}
	})

	it("decodes Issue payloads with nested parent references", () => {
		const decoded = decodeLinearIssueWebhookEvent({
			...issueWebhookPayload,
			data: {
				...issueWebhookPayload.data,
				parentId: "parent-entity-id",
				parent: {
					id: "parent-entity-id",
					identifier: "AZE-42",
				},
			},
		})
		expect(Option.isSome(decoded)).toBe(true)
		if (Option.isSome(decoded)) {
			expect(decoded.value.data.parent?.identifier).toBe("AZE-42")
		}
	})

	it("filters non-Issue payloads", () => {
		const decoded = decodeLinearIssueWebhookEvent({
			...issueWebhookPayload,
			type: "Comment",
		})
		expect(Option.isNone(decoded)).toBe(true)
	})
})

describe("Linear webhook signature verification", () => {
	it("verifies a valid signature and decodes the issue event", () => {
		const secret = "lin_wh_secret_test"
		const webhookClient = new LinearWebhookClient(secret)
		const rawBody = Buffer.from(JSON.stringify(issueWebhookPayload))
		const timestamp = String(Date.now())
		const signature = crypto.createHmac("sha256", secret).update(rawBody).digest("hex")

		const parsedPayload = webhookClient.parseData(rawBody, signature, timestamp)
		const decoded = decodeLinearIssueWebhookEvent(parsedPayload)

		expect(Option.isSome(decoded)).toBe(true)
	})

	it("rejects invalid signatures", () => {
		const secret = "lin_wh_secret_test"
		const webhookClient = new LinearWebhookClient(secret)
		const rawBody = Buffer.from(JSON.stringify(issueWebhookPayload))
		const timestamp = String(Date.now())

		expect(() => webhookClient.parseData(rawBody, "deadbeef", timestamp)).toThrow(
			"Invalid webhook signature",
		)
	})
})
