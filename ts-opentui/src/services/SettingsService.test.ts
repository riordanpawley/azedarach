import { describe, expect, it } from "bun:test"
import { EDITABLE_SETTINGS, getVisibleSettings } from "./SettingsService.js"

const issueSyncSetting = EDITABLE_SETTINGS.find((setting) => setting.key === "issueSyncEnabled")
const linearWebhooksSetting = EDITABLE_SETTINGS.find(
	(setting) => setting.key === "linearWebhooksEnabled",
)
const workflowModeSetting = EDITABLE_SETTINGS.find((setting) => setting.key === "workflowMode")
const specEnabledSetting = EDITABLE_SETTINGS.find((setting) => setting.key === "specEnabled")
const issueTrackerBackendSetting = EDITABLE_SETTINGS.find(
	(setting) => setting.key === "issueTrackerBackend",
)
const prEnabledSetting = EDITABLE_SETTINGS.find((setting) => setting.key === "prEnabled")
const cliToolSetting = EDITABLE_SETTINGS.find((setting) => setting.key === "cliTool")

if (issueSyncSetting === undefined) {
	throw new Error("Issue Sync setting definition is missing")
}

if (linearWebhooksSetting === undefined) {
	throw new Error("Linear Webhooks setting definition is missing")
}

if (workflowModeSetting === undefined) {
	throw new Error("Workflow mode setting definition is missing")
}

if (specEnabledSetting === undefined) {
	throw new Error("Spec enabled setting definition is missing")
}

if (issueTrackerBackendSetting === undefined) {
	throw new Error("Issue tracker backend setting definition is missing")
}

if (prEnabledSetting === undefined) {
	throw new Error("PR enabled setting definition is missing")
}

if (cliToolSetting === undefined) {
	throw new Error("CLI tool setting definition is missing")
}

describe("SettingsService issue sync setting", () => {
	it("treats missing issueTracker as disabled", () => {
		expect(issueSyncSetting.getValue({})).toBe(false)
	})

	it("first toggle on missing issueTracker enables local sync", () => {
		const toggled = issueSyncSetting.nextValue({})
		expect(toggled.issueTracker?.local?.syncEnabled).toBe(true)
	})

	it("toggles local sync from false to true", () => {
		const toggled = issueSyncSetting.nextValue({
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
		const toggled = linearWebhooksSetting.nextValue({
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
		expect(linearWebhooksSetting.nextValue(input)).toEqual(input)
		expect(linearWebhooksSetting.getValue(input)).toBe(false)
	})
})

describe("SettingsService non-boolean selectors", () => {
	it("defaults missing cliTool display value to codex", () => {
		expect(cliToolSetting.getValue({})).toBe("codex")
	})

	it("cycles workflow mode origin -> local -> origin", () => {
		const local = workflowModeSetting.nextValue({
			git: { workflowMode: "origin" },
		})
		expect(local.git?.workflowMode).toBe("local")

		const origin = workflowModeSetting.nextValue(local)
		expect(origin.git?.workflowMode).toBe("origin")
	})

	it("cycles issue tracker backend local -> tracker", () => {
		const toggled = issueTrackerBackendSetting.nextValue({
			issueTracker: {
				local: {
					syncEnabled: false,
				},
			},
		})
		expect(toggled.issueTracker?.tracker?.syncEnabled).toBe(true)
	})
})

describe("SettingsService spec feature setting", () => {
	it("defaults missing spec config to enabled", () => {
		expect(specEnabledSetting.getValue({})).toBe(true)
	})

	it("toggles spec feature from enabled to disabled", () => {
		const toggled = specEnabledSetting.nextValue({})
		expect(toggled.spec?.enabled).toBe(false)
	})
})

describe("SettingsService visibility gating", () => {
	it("hides linear webhook setting when backend is local", () => {
		const keys = getVisibleSettings({
			issueTracker: {
				local: {
					syncEnabled: false,
				},
			},
		}).map((setting) => setting.key)
		expect(keys.includes("linearWebhooksEnabled")).toBe(false)
	})

	it("hides PR defaults when PRs are disabled", () => {
		const withoutDefaults = prEnabledSetting.nextValue({
			pr: {
				enabled: true,
			},
		})
		const keys = getVisibleSettings(withoutDefaults).map((setting) => setting.key)
		expect(keys.includes("autoDraft")).toBe(false)
		expect(keys.includes("autoMerge")).toBe(false)
	})
})
