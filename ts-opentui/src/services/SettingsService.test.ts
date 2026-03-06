import { describe, expect, it } from "bun:test"
import { EDITABLE_SETTINGS } from "./SettingsService.js"

const issueSyncSetting = EDITABLE_SETTINGS.find((setting) => setting.key === "issueSyncEnabled")
const linearWebhooksSetting = EDITABLE_SETTINGS.find((setting) => setting.key === "linearWebhooksEnabled")

if (issueSyncSetting === undefined) {
	throw new Error("Issue Sync setting definition is missing")
}

if (linearWebhooksSetting === undefined) {
	throw new Error("Linear Webhooks setting definition is missing")
}

describe("SettingsService issue sync setting", () => {
	it("treats missing issueTracker as disabled", () => {
		expect(issueSyncSetting.getValue({})).toBe(false)
	})

	it("first toggle on missing issueTracker enables local sync", () => {
		const toggled = issueSyncSetting.toggle({})
		expect(toggled.issueTracker?.local?.syncEnabled).toBe(true)
	})

	it("toggles local sync from false to true", () => {
		const toggled = issueSyncSetting.toggle({
			issueTracker: {
				local: {
					syncEnabled: false,
				},
			},
		})
		expect(toggled.issueTracker?.local?.syncEnabled).toBe(true)
	})
})

describe("SettingsService linear webhooks setting", () => {
	it("defaults to true for linear backend when webhook setting is missing", () => {
		expect(
			linearWebhooksSetting.getValue({
				issueTracker: {
					linear: {},
				},
			}),
		).toBe(true)
	})

	it("toggles linear webhooks from true to false", () => {
		const toggled = linearWebhooksSetting.toggle({
			issueTracker: {
				linear: {
					webhooks: {
						enabled: true,
					},
				},
			},
		})
		expect(toggled.issueTracker?.linear?.webhooks?.enabled).toBe(false)
	})

	it("is a no-op for non-linear issue trackers", () => {
		const input = {
			issueTracker: {
				local: {
					syncEnabled: false,
				},
			},
		}
		expect(linearWebhooksSetting.toggle(input)).toEqual(input)
		expect(linearWebhooksSetting.getValue(input)).toBe(false)
	})
})
