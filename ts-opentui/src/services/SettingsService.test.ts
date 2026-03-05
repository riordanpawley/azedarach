import { describe, expect, it } from "bun:test"
import { EDITABLE_SETTINGS } from "./SettingsService.js"

const issueSyncSetting = EDITABLE_SETTINGS.find((setting) => setting.key === "issueSyncEnabled")

if (issueSyncSetting === undefined) {
	throw new Error("Issue Sync setting definition is missing")
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
